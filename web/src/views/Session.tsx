import * as i18n from "../i18n";
import { t } from "../i18n";
import { AgentMark } from "../agent-mark";
import { routeForTask, useCoreHost, useScheduleSessionStripView, useSessionView, type Go, type SessionWindowOptions } from "../core/host";
import { renderText } from "../core/text";
import { newRequestID } from "../crypto";
import {
  ComputerReachability,
  SessionCommandStatus,
  SessionDockKind,
  SessionInputNoticeKind,
  SessionNoteKind,
  SessionNoteTone,
  type SessionActionsView,
  type SessionCommandState,
  type SessionHeaderView,
  type SessionRestoreComposerEffect,
  type SessionTimelineItem,
  type SessionViewModel,
} from "../gen/remoteagent/core/v1/session_view_pb";
import { SessionDisplayStatus, SessionTone } from "../gen/remoteagent/core/v1/home_view_pb";
import { Agent, ApprovalMode, SendMode, SharedFileSchema, type ImageRef, type SharedFile } from "../gen/remoteagent/v1/event_pb";
import {
  CommandSchema,
  type Command,
} from "../gen/remoteagent/v1/remote_pb";
import {
  ArrowDown,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Ellipsis,
  Minus,
  X,
  Zap,
} from "../icons";
import { splitPlaceholders, timelineImages } from "../imageview";
import { Markdown } from "../markdown";
import { canAnimate, useExit, useKnob } from "../motion";
import { shortPath, shortenPath } from "../model";
import { type Remote } from "../remote";
import { previewBlobForFile, saveBlobAsDownload } from "../shared-files";
import { ModeSheet, RenameDialog, SessionSheet } from "../sheets";
import { skeletonMessage } from "../skeleton";
import { ActionSheet, Alert, Banner, ConnBar, Exp, NavBar, PathText, Sheet, StateMark, useRemote, type Mark } from "../ui";
import { getView, setView, type View } from "../view";
import { AttachButton, attachBlocked, maxImages, TrayView, useDropPaste, useTray } from "./Attach";
import { Composer } from "./Composer";
import { ApprovalPage, Dock, PlanPage, QuestionPage, type QuestionDraftState } from "./Dock";
import { FileChips, withoutFileLines } from "./Files";
import { HistoryOpening } from "./HistoryOpening";
import { HistoryRows } from "./HistoryRows";
import { GenCard, ImageStrip, ImageViewer, LocalThumbs, OpenImage } from "./Images";
import { OutgoingBubbles, PendingBubbles, type LocalSendAttachments } from "./Queue";
import { RememberDetails, RowState } from "./RowState";
import { ScheduleCreate, ScheduleStrip } from "./pages";
import { ScheduleText } from "./ScheduleText";
import { followsEnd, NEAR_END, type Scrolled } from "./scroll-follow";
import {
  SharedFileCards,
  SharedFilePreviewOverlay,
  SharedFileViewContext,
  type SharedFilePreviewTarget,
} from "./SharedFiles";
import { ToolCallView, ToolGroupView } from "./Tools";
import { create } from "@bufbuild/protobuf";
import { createPortal } from "react-dom";
import {
  lazy,
  memo,
  Suspense,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";

const TerminalView = lazy(() =>
  import("./Terminal").then((m) => ({ default: m.TerminalView })),
);

const DEFAULT_WINDOW_LIMIT = 80;
const CONTEXT_WARN = 70;
const EMPTY_ITEMS: SessionTimelineItem[] = [];
const EMPTY_REASONS: Array<{ id: string; reason: string }> = [];
const LOCAL_SCROLL = new WeakMap<object, Map<string, { top: number; stick: boolean }>>();
const SEND_ATTACHMENTS = new WeakMap<Remote, Map<string, LocalSendAttachments>>();
const LOCAL_DOCK_STATE = new WeakMap<object, { seen: Set<string>; closed: Set<string> }>();
function getDockState(remote?: Remote) {
  const key = remote ?? LOCAL_DOCK_STATE;
  let state = LOCAL_DOCK_STATE.get(key);
  if (!state) {
    state = { seen: new Set(), closed: new Set() };
    LOCAL_DOCK_STATE.set(key, state);
  }
  return state;
}

type Tab = View;

function getSendAttachments(remote?: Remote): Map<string, LocalSendAttachments> | undefined {
  if (!remote) return undefined;
  let m = SEND_ATTACHMENTS.get(remote);
  if (!m) {
    m = new Map();
    SEND_ATTACHMENTS.set(remote, m);
  }
  return m;
}

function sessionMark(header?: SessionHeaderView): Mark {
  if (!header) return "off";
  switch (header.status) {
    case SessionDisplayStatus.STARTING:
    case SessionDisplayStatus.RUNNING:
      return "run";
    case SessionDisplayStatus.APPROVAL:
    case SessionDisplayStatus.QUESTION:
    case SessionDisplayStatus.PLAN:
    case SessionDisplayStatus.POPUP:
      return "wait";
    case SessionDisplayStatus.ENDED:
    case SessionDisplayStatus.OFFLINE:
    case SessionDisplayStatus.UNLINKED:
      return "off";
    case SessionDisplayStatus.IDLE:
      return "idle";
  }
  switch (header.tone) {
    case SessionTone.LIVE:
      return "run";
    case SessionTone.WARN:
      return "wait";
    case SessionTone.BAD:
      return "off";
    default:
      return "idle";
  }
}

function commandErrorText(cmd?: SessionCommandState): string {
  if (!cmd) return "";
  if (cmd.status !== SessionCommandStatus.FAILED && cmd.status !== SessionCommandStatus.UNKNOWN) return "";
  return renderText(cmd.errorText) || renderText(cmd.statusText);
}

function useWidth<T extends HTMLElement>(): [(el: T | null) => void, number] {
  const [w, setW] = useState(0);
  const ro = useRef<ResizeObserver>(undefined);
  const ref = useCallback((el: T | null) => {
    ro.current?.disconnect();
    ro.current = undefined;
    if (!el) return;
    ro.current = new ResizeObserver(() => setW(el.clientWidth));
    ro.current.observe(el);
    setW(el.clientWidth);
  }, []);
  return [ref, w];
}

function useHeight(): [(el: HTMLElement | null) => void, number] {
  const [h, setH] = useState(0);
  const ro = useRef<ResizeObserver>(undefined);
  const ref = useCallback((el: HTMLElement | null) => {
    ro.current?.disconnect();
    if (!el) return;
    const o = new ResizeObserver(() => setH(el.clientHeight));
    o.observe(el);
    ro.current = o;
    setH(el.clientHeight);
  }, []);
  return [ref, h];
}

function ViewSwitch({
  className,
  tabs,
  tab,
  inactive,
  popup,
  onPick,
}: {
  className: string;
  tabs: [Tab, string][];
  tab: Tab;
  inactive: boolean;
  popup: boolean;
  onPick: (t: Tab) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useKnob(ref, tab);
  return (
    <div className={className} role="tablist" aria-label={i18n.t("session.view")} ref={ref}>
      <i className="mo-knob" aria-hidden />
      {tabs.map(([t, name]) => (
        <button
          key={t}
          role="tab"
          aria-selected={t === tab}
          className={t === tab ? "on" : ""}
          disabled={t !== "chat" && inactive}
          onClick={() => onPick(t)}
        >
          {name}
          {t === "term" && popup && <span className="n" />}
        </button>
      ))}
    </div>
  );
}

interface TimelineRowEntry {
  key: string;
  item: SessionTimelineItem;
}

function flattenTimelineItems(items: readonly SessionTimelineItem[]): TimelineRowEntry[] {
  const out: TimelineRowEntry[] = [];
  for (const item of items) {
    if (item.kind.case === "quietScheduledGroup" && item.kind.value.expanded) {
      for (const child of item.kind.value.items) {
        out.push({ key: child.id || `${child.seq}`, item: child });
      }
      continue;
    }
    out.push({ key: item.id || `${item.seq}`, item });
  }
  return out;
}

function itemEndSeq(item: SessionTimelineItem): bigint {
  return item.endSeq > item.seq ? item.endSeq : item.seq;
}

function recordRowSeen(seenKeys: Set<string>, r: TimelineRowEntry) {
  seenKeys.add(r.key);
  if (r.item.kind.case === "toolCall" && r.item.kind.value.callId) {
    seenKeys.add("tool:" + r.item.kind.value.callId);
  } else if (r.item.kind.case === "toolGroup") {
    for (const c of r.item.kind.value.calls) {
      if (c.callId) seenKeys.add("tool:" + c.callId);
    }
  }
}

function isRowSeen(r: TimelineRowEntry, seenKeys: Set<string>): boolean {
  if (seenKeys.has(r.key)) return true;
  if (r.item.kind.case === "toolCall" && r.item.kind.value.callId) {
    return seenKeys.has("tool:" + r.item.kind.value.callId);
  }
  if (r.item.kind.case === "toolGroup") {
    const firstCall = r.item.kind.value.calls[0];
    if (firstCall?.callId && seenKeys.has("tool:" + firstCall.callId)) return true;
  }
  return false;
}

function countUnseenRows(rows: readonly TimelineRowEntry[], seenWaterSeq: bigint, seenKeys: Set<string>): number {
  let unseen = 0;
  for (let i = rows.length - 1; i >= 0; i--) {
    const r = rows[i];
    const endSeq = itemEndSeq(r.item);
    if (endSeq === 0n) continue;
    if (endSeq <= seenWaterSeq) break;
    if (!isRowSeen(r, seenKeys)) unseen++;
  }
  return unseen;
}

export interface SessionScreenViewProps {
  vm: SessionViewModel;
  wide: boolean;
  onBack: () => void;
  go?: Go;
  remote?: Remote;
  scheduleTaskCount?: number;
  draft: string;
  onDraft: (v: string) => void;
  tray?: ReturnType<typeof useTray>;
  trayView?: ReactNode;
  attach?: ReactNode;
  dropping?: boolean;
  cardProps?: React.HTMLAttributes<HTMLDivElement>;
  composerRef?: RefObject<HTMLTextAreaElement | null>;
  composerKey?: number;
  approvalReason?: string;
  onApprovalReasonChange?: (approvalId: string, v: string) => void;
  localAttachments?: Map<string, LocalSendAttachments>;
  toast?: string;
  onClearToast?: () => void;
  sentCount?: number;
  defaultTab?: Tab;
  defaultPage?: "" | "plan" | "question" | { approval: string };
  defaultMenu?: boolean;
  defaultSendMenu?: boolean;
  defaultDenying?: boolean;
  defaultExpandedQueueAll?: boolean;
  onSend: (text: string, mode: SendMode) => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onAbandonCommand: (slotKey: string) => void;
  onDismissOutgoing: (requestId: string) => void;
  onRestoreOutgoing: (requestId: string) => void;
  onRestoreText?: (text: string, requestId?: string) => void;
  onLoadEarlier: () => void;
  onRetryEvents?: () => void;
  onRefreshModels?: () => void;
  onExpandGroup: (groupId: string) => void;
  onScheduleRevealed?: () => void;
}

export function SessionScreenView({
  vm,
  wide,
  onBack,
  go,
  remote,
  scheduleTaskCount = 0,
  draft,
  onDraft,
  tray,
  trayView,
  attach,
  dropping = false,
  cardProps,
  composerRef: externalComposerRef,
  composerKey = 0,
  approvalReason,
  onApprovalReasonChange,
  localAttachments,
  toast = "",
  onClearToast,
  sentCount = 0,
  defaultTab,
  defaultPage = "",
  defaultMenu = false,
  defaultSendMenu = false,
  defaultDenying = false,
  defaultExpandedQueueAll = false,
  onSend,
  onSubmitCommand,
  onAbandonCommand,
  onDismissOutgoing,
  onRestoreOutgoing,
  onRestoreText,
  onLoadEarlier,
  onRetryEvents,
  onRefreshModels,
  onExpandGroup,
  onScheduleRevealed,
}: SessionScreenViewProps) {
  const sid = vm.sessionId;
  const header = vm.header;
  const actions = vm.actions;
  const dock = vm.dock;
  const composer = vm.composer;
  const timeline = vm.timeline;

  const [rootRef, width] = useWidth<HTMLDivElement>();
  const [colRef, colHeight] = useHeight();
  const room = colHeight > 0 ? colHeight - 230 : Infinity;
  const canSplit = wide && width >= 840;

  const hasContent =
    (vm.timeline?.items.length ?? 0) > 0 ||
    (vm.composer?.pendingInputs.length ?? 0) > 0 ||
    (vm.composer?.outgoing.length ?? 0) > 0;
  const pending = (!header || (!header.found && !header.missing)) && !hasContent;
  const exited = !!header?.exited;
  const reachabilityUnknown = (() => {
    return header?.computerReachability === ComputerReachability.UNKNOWN;
  })();
  const inactive = exited || (!!header?.found && !header.computerOnline && !reachabilityUnknown) || (!!header?.computerUnlinked && !reachabilityUnknown);
  const [questionDraft, setQuestionDraft] = useState<QuestionDraftState | undefined>(undefined);
  const seenApprovals = useRef(new Set<string>());
  const resolvedByUs = useRef(new Set<string>());
  const currentApprovalId = dock?.hasItem && dock.kind === SessionDockKind.APPROVAL && dock.detail.case === "approval" ? dock.detail.value.approvalId : "";
  if (currentApprovalId) {
    seenApprovals.current.add(currentApprovalId);
    if (dock?.command?.busy) resolvedByUs.current.add(currentApprovalId);
  }
  const retainedReasons = useMemo(() => {
    if (!remote || !timeline?.loaded || remote.approvalReasons.size === 0) return EMPTY_REASONS;
    const out: Array<{ id: string; reason: string }> = [];
    for (const [id, reason] of remote.approvalReasons) {
      if (reason && id !== currentApprovalId && seenApprovals.current.has(id) && !resolvedByUs.current.has(id)) {
        out.push({ id, reason });
      }
    }
    return out.length > 0 ? out : EMPTY_REASONS;
  }, [remote, timeline?.loaded, currentApprovalId, remote?.approvalReasons.size]);

  const [tabPick, setTab] = useState<Tab>(() => {
    if (defaultTab) return defaultTab;
    if (remote?.termFirst.delete(sid)) return wide ? "split" : "term";
    return !header?.found ? "chat" : getView();
  });
  const pickTab = (t: Tab) => {
    setTab(t);
    if (remote) setView(t);
  };
  let tab: Tab = tabPick === "split" && !canSplit ? "chat" : tabPick;
  if ((exited || pending) && tab !== "chat") tab = "chat";
  const toTerminal = useCallback(() => setTab(canSplit ? "split" : "term"), [canSplit]);

  const [sheet, setSheet] = useState("" as "" | "mode" | "model");
  const [menu, setMenu] = useState(defaultMenu);
  const [scheduling, setScheduling] = useState(false);
  const [confirmEnd, setConfirmEnd] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [dockDenying, setDockDenying] = useState(defaultDenying);
  const [dockOpen, setDockOpen] = useState(false);
  const [directOpen, setDirectOpen] = useState(false);
  const directBtnRef = useRef(null as HTMLButtonElement | null);
  const [popPos, setPopPos] = useState(undefined as { top: number; left: number } | undefined);
  const [page, setPage] = useState(defaultPage as "" | "plan" | "question" | { approval: string });
  useEffect(() => {
    setMenu(false);
    setDirectOpen(false);
    setDockDenying(defaultDenying);
    setPage(defaultPage);
  }, [sid, defaultPage, defaultDenying]);

  const directStatus = remote && header?.machineId ? remote.direct.status(header.machineId) : undefined;
  const directActive =
    !exited &&
    !!header?.computerOnline &&
    !header?.computerUnlinked &&
    directStatus?.state === "connected" &&
    (directStatus.read === "direct" || directStatus.file === "direct");
  const showDirectChip = directActive || directOpen;

  useLayoutEffect(() => {
    if (!directOpen) return;
    const place = () => {
      const el = directBtnRef.current;
      if (!el || typeof window === "undefined") return;
      const rect = el.getBoundingClientRect();
      const w = Math.min(332, Math.max(220, window.innerWidth - 40));
      const left = Math.max(20, Math.min(rect.left + rect.width / 2 - w / 2, window.innerWidth - w - 20));
      setPopPos({ top: rect.bottom + 8, left });
    };
    place();
    window.addEventListener("resize", place);
    return () => window.removeEventListener("resize", place);
  }, [directOpen]);

  useEffect(() => {
    if (!directOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        setDirectOpen(false);
        directBtnRef.current?.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [directOpen]);
  const forkCmd = actions?.fork;
  const forkErr = commandErrorText(forkCmd);
  const wasForkBusy = useRef(false);
  useEffect(() => {
    if (forkCmd?.busy) {
      wasForkBusy.current = true;
      return;
    }
    if (wasForkBusy.current) {
      wasForkBusy.current = false;
      if (!forkErr && forkCmd?.status !== SessionCommandStatus.UNKNOWN && !forkCmd?.expired) {
        setMenu(false);
      }
    }
  }, [forkCmd?.busy, forkCmd?.status, forkCmd?.expired, forkErr]);
  const internalComposerRef = useRef<HTMLTextAreaElement>(null);
  const composerElRef = externalComposerRef ?? internalComposerRef;
  const { seen: dockSeen, closed: dockClosed } = getDockState(remote);

  const [viewing, setViewing] = useState<number | undefined>(undefined);
  const [viewingShared, setViewingShared] = useState<SharedFilePreviewTarget | undefined>(undefined);
  const items = timeline?.items ?? EMPTY_ITEMS;
  const pictures = useMemo(() => timelineImages(items), [items]);
  const openImage = useCallback(
    (ref: ImageRef) => {
      const i = pictures.findIndex((p) => p.ref === ref);
      if (i >= 0) setViewing(i);
    },
    [pictures],
  );

  const openSharedFile = useCallback((file: SharedFile, blob: Blob) => {
    const preview = previewBlobForFile(blob, file.name);
    if (!preview) {
      saveBlobAsDownload(blob, file.name);
      return;
    }
    setViewingShared({
      file,
      blob,
      kind: preview.kind,
      previewBlob: preview.blob,
      path: file.path,
    });
  }, []);

  useEffect(() => {
    if (!remote) return;
    remote.sharedFiles.setCurrentSession(sid);
    remote.sharedFiles.setAutoOpenHandler((targetSid, file, blob) => {
      if (targetSid === sid) openSharedFile(file, blob);
    });
    return () => {
      remote.sharedFiles.setCurrentSession(undefined);
      remote.sharedFiles.setAutoOpenHandler(undefined);
    };
  }, [remote, sid, openSharedFile]);

  const sharedFileCtx = useMemo(
    () => ({
      openFile: openSharedFile,
      onToast: (_msg: string) => undefined,
    }),
    [openSharedFile],
  );

  const missing = !!header?.missing;
  if (missing) {
    return (
      <div className="page">
        <NavBar onBack={onBack} />
        <div className="center">
          <div>
            <div className="et">{i18n.t("session.ios_missing")}</div>
            <div className="muted small">
              {renderText(header?.missingText) || i18n.t("errors.it_may_have_been_deleted_or_belong_to_a")}
            </div>
            <div className="big">
              <button className="btn pri" onClick={onBack}>
                {i18n.t("common.back")}
              </button>
            </div>
          </div>
        </div>
      </div>
    );
  }

  const title = renderText(header?.title) || i18n.t("common.loading");
  const agent = header?.agent ?? Agent.UNSPECIFIED;
  const agentLabel = header?.agentName || "";
  const mname = renderText(header?.computerName);
  const project = renderText(header?.projectName);

  if (page === "plan" && dock?.kind === SessionDockKind.PLAN && dock.detail.case === "plan") {
    return (
      <PlanPage
        sessionId={sid}
        dock={dock}
        plan={dock.detail.value}
        canSend={header?.capabilities?.canSend ?? true}
        title={title}
        onBack={() => setPage("")}
        onSubmitCommand={onSubmitCommand}
        onAbandonCommand={onAbandonCommand}
      />
    );
  }
  if (page === "question" && dock?.kind === SessionDockKind.QUESTION && dock.detail.case === "question") {
    return (
      <QuestionPage
        sessionId={sid}
        dock={dock}
        q={dock.detail.value}
        canSend={header?.capabilities?.canSend ?? true}
        agentName={agentLabel}
        title={title}
        questionDraft={questionDraft}
        onQuestionDraftChange={setQuestionDraft}
        onBack={() => setPage("")}
        onTerminal={() => {
          setPage("");
          toTerminal();
        }}
        onSubmitCommand={onSubmitCommand}
        onAbandonCommand={onAbandonCommand}
      />
    );
  }
  if (typeof page === "object" && "approval" in page && dock?.kind === SessionDockKind.APPROVAL && dock.detail.case === "approval") {
    const ap = dock.detail.value;
    if (ap.approvalId === page.approval) {
      return (
        <ApprovalPage
          sessionId={sid}
          dock={dock}
          approval={ap}
          canSend={header?.capabilities?.canSend ?? true}
          title={title}
          reason={approvalReason}
          onReasonChange={onApprovalReasonChange ? (v) => onApprovalReasonChange(ap.approvalId, v) : undefined}
          onBack={() => setPage("")}
          onSubmitCommand={onSubmitCommand}
          onAbandonCommand={onAbandonCommand}
        />
      );
    }
  }

  const modeLabel = renderText(header?.approvalModeLabel);
  const modeTail =
    !exited && modeLabel ? (
      <>
        {" "}
        · <span className={header?.approvalMode === ApprovalMode.FULL_ACCESS ? "red" : ""}>{modeLabel}</span>
      </>
    ) : null;
  const directChip = showDirectChip ? (
    <span className="dchip-wrap">
      {!wide && " · "}
      <button
        ref={directBtnRef}
        type="button"
        className="dchip"
        aria-expanded={directOpen}
        aria-label={t("transport.popover_a11y")}
        onClick={() => setDirectOpen((v) => !v)}
      >
        <Zap />
        <span>{t("transport.direct")}</span>
      </button>
    </span>
  ) : null;
  const statusText = pending ? t("common.loading") : renderText(header?.statusText);
  const sub = (
    <>
      {pending ? <span data-late="1">{statusText}</span> : statusText}
      {modeTail}
      {directChip}
    </>
  );

  const pct = header?.hasContext ? header.contextPercent : undefined;
  const high = !!header?.highContextWarning || (pct !== undefined && pct > CONTEXT_WARN);
  const modelText = [header?.modelLabel, header?.effort].filter(Boolean).join(" · ");
  const moreLabel = high && pct !== undefined ? i18n.t("session.more_context", { percent: pct }) : i18n.t("common.android_more");
  const tabs: [Tab, string][] = canSplit
    ? [
        ["chat", i18n.t("session.chat")],
        ["split", i18n.t("session.split")],
        ["term", i18n.t("session.terminal")],
      ]
    : [
        ["chat", i18n.t("session.chat")],
        ["term", i18n.t("session.terminal")],
      ];
  const popup = dock?.hasItem && dock.kind === SessionDockKind.TERMINAL_POPUP;

  const seg = (
    <ViewSwitch
      className={"seg" + (tab === "term" && !wide ? " ondark" : "")}
      tabs={tabs}
      tab={tab}
      inactive={exited}
      popup={!!popup}
      onPick={pickTab}
    />
  );

  const headerBar = wide ? (
    <header className="dh">
      {header?.isScheduled && (
        <button className="schedule-run-back" aria-label={t("common.back")} title={i18n.t("session.back_task")} onClick={onBack}>
          <ChevronLeft />
        </button>
      )}
      <b>{title}</b>
      <span className="st">
        <StateMark kind={sessionMark(header)} />
        <span>
          {statusText}
          {modeTail}
        </span>
        {directChip}
      </span>
      <span className="sp" />
      {seg}
      <button
        className={"ib mr" + (high ? " context-high" : "")}
        onClick={() => setMenu(true)}
        disabled={pending}
        aria-label={moreLabel}
      >
        <Ellipsis />
      </button>
    </header>
  ) : (
    <NavBar
      onBack={onBack}
      title={title}
      sub={sub}
      right={
        <button
          className={"more" + (high ? " context-high" : "")}
          onClick={() => setMenu(true)}
          disabled={pending}
          aria-label={moreLabel}
        >
          <Ellipsis />
        </button>
      }
      className={tab === "term" ? "dark" : ""}
    />
  );

  const interruptCmd = actions?.interrupt;
  const endCmd = actions?.end;
  const forkBlockedReason = renderText(actions?.forkBlockedReason);

  const chatCol = (
    <div className="ccol" ref={colRef}>
      <ChatBody
        vm={vm}
        remote={remote}
        sent={sentCount}
        localAttachments={localAttachments}
        retainedReasons={retainedReasons}
        defaultExpandedQueueAll={defaultExpandedQueueAll}
        onRestoreText={onRestoreText}
        onTerminal={exited ? undefined : toTerminal}
        onSubmitCommand={onSubmitCommand}
        onRetryOutgoing={onSubmitCommand}
        onDismissOutgoing={onDismissOutgoing}
        onRestoreOutgoing={onRestoreOutgoing}
        onLoadEarlier={onLoadEarlier}
        onRetryEvents={onRetryEvents}
        onExpandGroup={onExpandGroup}
        onScheduleRevealed={onScheduleRevealed}
      />
      {toast && (
        <div className="toast" onClick={onClearToast}>
          {toast}
        </div>
      )}
      {!inactive && (
        <Dock
          sessionId={sid}
          dock={dock}
          header={header}
          drafting={!!draft.trim() || (tray?.items.length ?? 0) > 0}
          room={room}
          height={colHeight}
          seenKeys={dockSeen}
          closedKeys={dockClosed}
          defaultDenying={defaultDenying}
          reason={approvalReason}
          onReasonChange={onApprovalReasonChange}
          questionDraft={questionDraft}
          onQuestionDraftChange={setQuestionDraft}
          onDenyingChange={setDockDenying}
          onOpenChange={setDockOpen}
          onExpand={() => composerElRef.current?.blur()}
          onDetail={(id) => setPage({ approval: id })}
          onPlan={() => setPage("plan")}
          onQuestion={() => setPage("question")}
          onTerminal={toTerminal}
          onSubmitCommand={onSubmitCommand}
          onAbandonCommand={onAbandonCommand}
        />
      )}
      {header?.isScheduled && <div className="schedule-reply-hint">{i18n.t("session.reply_moves")}</div>}
      {inactive ? (
        <EndCard
          sessionId={sid}
          header={header}
          actions={actions}
          composer={composer}
          onSubmitCommand={onSubmitCommand}
        />
      ) : null}
      {!inactive && !dockDenying && (
        <Composer
          key={composerKey}
          ref={composerElRef}
          composer={composer}
          onSend={onSend}
          onInterrupt={() =>
            onSubmitCommand(
              interruptCmd?.slotKey ?? "",
              create(CommandSchema, {
                kind: { case: "interrupt", value: { sessionId: sid } },
              }),
            )
          }
          draft={draft}
          onDraft={onDraft}
          tray={tray}
          trayView={trayView}
          attach={attach}
          cardProps={cardProps}
          dropping={dropping}
          interruptDisabled={!!interruptCmd?.busy}
          defaultSendMenu={defaultSendMenu}
          demoted={dockOpen}
        />
      )}
    </div>
  );

  const forkAction = () =>
    onSubmitCommand(
      forkCmd?.slotKey ?? "",
      create(CommandSchema, {
        kind: { case: "forkSession", value: { sessionId: sid } },
      }),
    );

  const menuActions = [
    ...(!exited && modeLabel
      ? [
          {
            label: i18n.t("modes.title"),
            value: modeLabel,
            red: header?.approvalMode === ApprovalMode.FULL_ACCESS,
            onClick: () => setSheet("mode"),
          },
        ]
      : []),
    ...(!exited
      ? [
          {
            label: modelText ? i18n.t("models.title") : i18n.t("session.settings"),
            value: modelText,
            onClick: () => setSheet("model"),
          },
        ]
      : []),
    ...(!exited && pct !== undefined
      ? [
          {
            label: i18n.t("session.context"),
            value: i18n.t("session.context_used", { percent: pct }),
            warn: high,
            onClick: () => setSheet("model"),
          },
        ]
      : []),
    ...(actions?.canRename
      ? [{ label: i18n.t("session.rename"), group: 1, onClick: () => setRenaming(true) }]
      : []),
    ...(header?.capabilities?.supportsFork && header?.computerOnline && !header?.computerUnlinked
      ? [
          ...(forkCmd?.busy
            ? [
                {
                  label: i18n.t("session.forking"),
                  busy: true,
                  keep: true,
                  group: 1,
                  onClick: () => undefined,
                },
              ]
            : !actions?.canFork && forkBlockedReason
              ? [
                  {
                    label: i18n.t("session.fork"),
                    disabled: true,
                    note: forkBlockedReason,
                    group: 1,
                    onClick: () => undefined,
                  },
                ]
              : forkCmd?.expired
                ? [
                    {
                      label: i18n.t("session.abandon_fork"),
                      keep: true,
                      note: i18n.t("session.fork_expired"),
                      noteRed: true,
                      group: 1,
                      onClick: () => onAbandonCommand(forkCmd.slotKey),
                    },
                  ]
                : [
                    {
                      label: i18n.t("session.fork"),
                      keep: true,
                      note:
                        forkCmd?.status === SessionCommandStatus.UNKNOWN
                          ? i18n.t("session.fork_unknown")
                          : forkErr
                            ? i18n.t("session.fork_failed", { reason: forkErr })
                            : undefined,
                      noteRed: true,
                      group: 1,
                      onClick: forkAction,
                    },
                    ...(forkCmd?.canAbandon
                      ? [
                          {
                            label: i18n.t("session.abandon_fork"),
                            keep: true,
                            group: 1,
                            onClick: () => onAbandonCommand(forkCmd.slotKey),
                          },
                        ]
                      : []),
                  ]),
        ]
      : []),
    ...(remote && header
      ? [
          {
            label: i18n.t("session.schedule_send"),
            group: 1,
            disabled: !header?.capabilities?.supportsSchedule || !remote.peer(header?.machineId).reachable || !remote.peer(header?.machineId).ready,
            note: !header?.capabilities?.supportsSchedule ? i18n.t("session.update_computer") : undefined,
            onClick: () => setScheduling(true),
          },
        ]
      : []),
    ...(!inactive
      ? [
          actions?.canInterrupt
            ? {
                label: i18n.t("session.interrupt_turn"),
                red: true,
                group: 1,
                onClick: () =>
                  onSubmitCommand(
                    interruptCmd?.slotKey ?? "",
                    create(CommandSchema, {
                      kind: { case: "interrupt", value: { sessionId: sid } },
                    }),
                  ),
              }
            : {
                label: i18n.t("session.interrupt_turn"),
                disabled: true,
                note: i18n.t("session.not_running"),
                group: 1,
                onClick: () => undefined,
              },
        ]
      : []),
    ...(actions?.canEnd
      ? [
          {
            label: i18n.t("session.end_action"),
            red: true,
            group: 2,
            onClick: () => setConfirmEnd(true),
          },
        ]
      : []),
  ].map((a) => ("busy" in a || !forkCmd?.busy ? a : { ...a, disabled: true }));

  return (
    <SharedFileViewContext.Provider value={sharedFileCtx}>
      <OpenImage.Provider value={openImage}>
        <div className={"session" + (tab === "term" && !wide ? " termmode" : "")} ref={rootRef}>
          {headerBar}
          {remote && <ConnBar remote={remote} />}
          {remote && scheduleTaskCount > 0 && (
            <Suspense fallback={<div className="schedule-strip" aria-hidden />}>
              <ScheduleStrip
                remote={remote}
                sessionId={sid}
                go={(route) => {
                  location.hash = "/" + route;
                }}
              />
            </Suspense>
          )}
          {!wide && <div className="segrow">{seg}</div>}
          <div className={"panes p-" + tab}>
            {tab !== "term" && chatCol}
            {tab !== "chat" && remote && (
              <div className="tcol">
                <Suspense fallback={null}>
                  <TerminalView
                    remote={remote}
                    session={{ id: sid, machineId: header?.machineId ?? "" }}
                    compact={tab === "split"}
                    wide={wide}
                    running={!exited}
                  />
                </Suspense>
              </div>
            )}
          </div>
          {menu && (
            <ActionSheet
              info={
                <>
                  <b>
                    <AgentMark agent={agent} named />
                    {[agentLabel, project, mname].filter(Boolean).join(" · ")}
                  </b>
                  <span className="mono">
                    <PathText path={shortPath(header?.cwd ?? "")} />
                  </span>
                </>
              }
              actions={menuActions}
              pop={wide}
              onClose={() => setMenu(false)}
            />
          )}
          {scheduling && remote && header && (
            <Sheet className="schedule-session-sheet" onClose={() => setScheduling(false)}>
              <div className="schedule-session-overlay">
                <Suspense fallback={null}>
                  <ScheduleCreate
                    remote={remote}
                    sessionId={sid}
                    go={(route) => {
                      setScheduling(false);
                      location.hash = "/" + route;
                    }}
                    onBack={() => setScheduling(false)}
                  />
                </Suspense>
              </div>
            </Sheet>
          )}
          {confirmEnd && (
            <Alert
              title={i18n.t("session.end_title")}
              text={renderText(actions?.endConfirmNote) || i18n.t("session.end_note", { name: mname, agent: agentLabel })}
              ok={i18n.t("session.end")}
              danger
              onCancel={() => setConfirmEnd(false)}
              onOk={() => {
                setConfirmEnd(false);
                onSubmitCommand(
                  endCmd?.slotKey ?? "",
                  create(CommandSchema, {
                    kind: { case: "stopSession", value: { sessionId: sid } },
                  }),
                );
              }}
            />
          )}
          {renaming && (
            <RenameDialog
              sessionId={sid}
              header={header}
              actions={actions}
              onClose={() => setRenaming(false)}
              onSubmitCommand={onSubmitCommand}
            />
          )}
          {sheet === "mode" && (
            <ModeSheet
              sessionId={sid}
              header={header}
              actions={actions}
              onClose={() => setSheet("")}
              onSubmitCommand={onSubmitCommand}
            />
          )}
          {sheet === "model" && (
            <SessionSheet
              sessionId={sid}
              header={header}
              actions={actions}
              onClose={() => setSheet("")}
              onRefreshModels={() => onRefreshModels?.()}
              onSubmitCommand={onSubmitCommand}
              onAbandonCommand={onAbandonCommand}
            />
          )}
          {viewing !== undefined && remote && (
            <ImageViewer
              remote={remote}
              sid={sid}
              images={pictures}
              start={viewing}
              agent={agentLabel}
              title={title}
              wide={wide}
              onClose={() => setViewing(undefined)}
              showPath={(p) => shortenPath(p, header?.cwd ?? "", "")}
            />
          )}
          {viewingShared && remote && (
            <SharedFilePreviewOverlay
              remote={remote}
              sessionId={sid}
              agentName={agentLabel}
              wide={wide}
              target={viewingShared}
              onClose={() => setViewingShared(undefined)}
            />
          )}
          {directOpen && directStatus && header && typeof document !== "undefined" && createPortal(
            <>
              <div className="dpop-back" onClick={() => setDirectOpen(false)} />
              <div
                className="dpop"
                role="dialog"
                aria-label={t("transport.direct")}
                style={popPos ? { top: `${popPos.top}px`, left: `${popPos.left}px` } : { top: "56px", left: "20px" }}
              >
                <div className="dpop-hd">
                  <StateMark kind={directActive ? "online" : "off"} />
                  <span>
                    {directActive
                      ? directStatus.read === "direct" && directStatus.readMs.direct !== undefined
                        ? t("transport.popover_direct_title_rtt", { name: mname, ms: String(Math.round(directStatus.readMs.direct)) })
                        : t("transport.popover_direct_title", { name: mname })
                      : directStatus.readMs.relay !== undefined
                        ? t("transport.popover_relay_title_rtt", { name: mname, ms: String(Math.round(directStatus.readMs.relay)) })
                        : t("transport.popover_relay_title", { name: mname })}
                  </span>
                </div>
                <p className="dpop-body">
                  {directActive ? t("transport.popover_direct_body") : t("transport.popover_relay_body")}
                </p>
                <div className="dpop-foot">
                  <span>
                    {!directActive
                      ? t("transport.popover_switched_relay")
                      : directStatus.read === "direct" && directStatus.file === "direct"
                        ? t("transport.popover_stable")
                        : directStatus.read === "direct"
                          ? t("transport.popover_read_only")
                          : t("transport.popover_file_only")}
                  </span>
                  {go && (
                    <button
                      type="button"
                      onClick={() => {
                        setDirectOpen(false);
                        go(`machine/${encodeURIComponent(header.machineId)}/connection`);
                      }}
                    >
                      {t("transport.popover_settings")}
                      <ChevronRight />
                    </button>
                  )}
                </div>
              </div>
            </>,
            document.body,
          )}
        </div>
      </OpenImage.Provider>
    </SharedFileViewContext.Provider>
  );
}

export function SessionPage({
  remote,
  sessionId,
  onBack,
  wide,
  go,
}: {
  remote: Remote;
  sessionId: string;
  onBack: () => void;
  wide: boolean;
  go?: Go;
}) {
  useRemote(remote);
  const host = useCoreHost(remote, go);
  const [windowLimit, setWindowLimit] = useState(DEFAULT_WINDOW_LIMIT);
  const [expandedGroups, setExpandedGroups] = useState<string[]>([]);

  useEffect(() => {
    setWindowLimit(DEFAULT_WINDOW_LIMIT);
    setExpandedGroups([]);
  }, [sessionId]);

  useEffect(() => {
    host.scheduleSetVisibleSession(sessionId);
    return () => host.scheduleSetVisibleSession("");
  }, [host, sessionId]);

  const scheduleFocus = host.getScheduleFocus(sessionId);
  const stripVm = useScheduleSessionStripView(host, sessionId);
  const windowOpts = useMemo<SessionWindowOptions>(
    () => ({
      limit: windowLimit,
      revealSeq: scheduleFocus?.seq ?? 0n,
      revealRunId: scheduleFocus?.runId ?? "",
      expandedGroupIds: expandedGroups,
    }),
    [windowLimit, scheduleFocus?.seq, scheduleFocus?.runId, expandedGroups],
  );

  const vm = useSessionView(host, sessionId, windowOpts, { refreshModels: true, refreshSettings: true });
  const header = vm.header;
  const machineId = header?.machineId ?? "";
  const machine = remote.machines.get(machineId);

  const [toast, setToast] = useState("");
  const toastRef = useRef(setToast);
  toastRef.current = setToast;
  const [sent, setSent] = useState(0);
  const [, setDraftVer] = useState(0);
  const [composerKey, setComposerKey] = useState(0);
  const composerRef = useRef<HTMLTextAreaElement>(null);

  const tray = useTray(remote, sessionId, machineId, maxImages(remote, machine));
  const attachWhy = attachBlocked(remote, machine);
  const drop = useDropPaste(tray, attachWhy, toastRef.current);
  const stash = getSendAttachments(remote);
  const pendingRestoreFiles = useRef(new Map<string, LocalSendAttachments["files"]>());
  const mountedSession = useRef(true);
  useEffect(() => {
    mountedSession.current = true;
    return () => {
      mountedSession.current = false;
    };
  }, []);

  useEffect(() => {
    return host.onRestoreComposer(sessionId, (eff: SessionRestoreComposerEffect) => {
      const saved = stash?.get(eff.requestId);
      const files: NonNullable<LocalSendAttachments["files"]> =
        saved?.files ??
        pendingRestoreFiles.current.get(eff.requestId) ??
        eff.fileIds.map((fileId) => ({ fileId, name: "", sizeBytes: 0n }));
      pendingRestoreFiles.current.delete(eff.requestId);
      if (files.length) {
        const fileIssue = remote.fileUploadIssue(machineId);
        if (fileIssue) {
          toastRef.current(fileIssue);
          return;
        }
      }
      if (tray.files().length + files.length > tray.maxFiles) {
        toastRef.current(i18n.t("composer.restore_files_full", { count: files.length }));
        return;
      }
      if (
        eff.imageIds.length &&
        !tray.restore(
          eff.imageIds.map((imageId, i) => ({
            imageId,
            thumb: saved?.thumbs?.[i],
            file: saved?.items?.filter((it) => it.kind !== "file")[i]?.file,
          })),
          !!saved?.original,
        )
      ) {
        toastRef.current(
          i18n.t("composer.restore_images_full", { existing: tray.images().length, count: eff.imageIds.length }),
        );
        return;
      }
      if (files.length) {
        tray.restoreFiles(files.map((f) => ({ fileId: f.fileId, name: f.name, size: Number(f.sizeBytes), file: f.file })));
      }
      const existingDraft = remote.drafts.get(sessionId) ?? "";
      remote.drafts.set(sessionId, existingDraft.trim() ? existingDraft.trimEnd() + "\n\n" + eff.text : eff.text);
      stash?.delete(eff.requestId);
      setComposerKey((k) => k + 1);
      setTimeout(() => composerRef.current?.focus());
    });
  }, [host, remote, sessionId, machineId, tray, stash]);

  // Mirror slotKey -> Core requestId so local attachment previews follow across outgoing -> pendingInputs -> inputState notices.
  useEffect(() => {
    if (!stash || stash.size === 0) return;
    for (const o of vm.composer?.outgoing ?? []) {
      if (o.slotKey && o.requestId && !stash.has(o.requestId)) {
        const bySlot = stash.get(o.slotKey);
        if (bySlot) stash.set(o.requestId, bySlot);
      }
    }
  }, [stash, vm.composer?.outgoing]);

  const handleSend = (text: string, mode: SendMode) => {
    const reqId = newRequestID();
    const slotKey = `send:${sessionId}:${reqId}`;
    const imgIds = tray.imageIds();
    const files = tray.files().map((f) => ({
      fileId: f.fileId!,
      name: f.name!,
      sizeBytes: BigInt(f.bytes ?? 0),
      file: f.file instanceof File ? f.file : undefined,
    }));
    const fileIds = files.map((f) => f.fileId);
    if (stash) {
      const saved = {
        thumbs: tray.images().map((i) => i.thumb!).filter(Boolean),
        imageSizes: tray.images().map((i) => ({ width: i.prepared!.width, height: i.prepared!.height })),
        files,
        items: [...tray.items],
        original: tray.original,
      };
      stash.set(reqId, saved);
      stash.set(slotKey, saved);
    }
    host.sessionSubmitCommand(
      slotKey,
      machineId,
      sessionId,
      create(CommandSchema, {
        kind: {
          case: "send",
          value: {
            sessionId,
            requestId: reqId,
            text,
            mode,
            imageIds: imgIds,
            fileIds,
          },
        },
      }),
    );
    tray.clear();
    remote.drafts.delete(sessionId);
    setSent((n) => n + 1);
  };

  const handleRestoreOutgoing = async (reqId: string) => {
    const og = (vm.composer?.outgoing ?? []).find((o) => o.requestId === reqId);
    const saved = stash?.get(reqId) ?? (og?.slotKey ? stash?.get(og.slotKey) : undefined);
    if (saved && !stash?.has(reqId)) stash?.set(reqId, saved);
    const files =
      saved?.files ?? (og?.fileIds ?? []).map((fileId) => ({ fileId, name: "", sizeBytes: 0n }));
    try {
      await remote.restoreDraft(sessionId, tray);
    } catch {
      return;
    }
    if (!mountedSession.current) return;
    if (files.length) {
      const fileIssue = remote.fileUploadIssue(machineId);
      if (fileIssue) {
        toastRef.current(fileIssue);
        return;
      }
      if (tray.files().length + files.length > tray.maxFiles) {
        toastRef.current(i18n.t("composer.restore_files_full", { count: files.length }));
        return;
      }
      pendingRestoreFiles.current.set(reqId, files);
    }
    if ((og?.imageIds.length ?? 0) > 0 && tray.images().length + og!.imageIds.length > tray.max) {
      toastRef.current(
        i18n.t("composer.restore_images_full", { existing: tray.images().length, count: og!.imageIds.length }),
      );
      return;
    }
    host.sessionRestoreOutgoing(sessionId, reqId);
  };

  const handleBack = () => {
    if (go && header?.isScheduled && header.scheduledTaskId && header.machineId) {
      go(routeForTask(header.machineId, header.scheduledTaskId));
      return;
    }
    onBack();
  };

  const approvalId = vm.dock?.detail.case === "approval" ? vm.dock.detail.value.approvalId : "";
  const approvalReason = approvalId ? (remote.approvalReasons.get(approvalId) ?? "") : "";

  const timelineRef = useRef(vm.timeline);
  timelineRef.current = vm.timeline;
  const restoreOutgoingRef = useRef(handleRestoreOutgoing);
  restoreOutgoingRef.current = handleRestoreOutgoing;

  const onSubmitCommand = useCallback(
    (slotKey: string, cmd: Command) => host.sessionSubmitCommand(slotKey, machineId, sessionId, cmd),
    [host, machineId, sessionId],
  );
  const onAbandonCommand = useCallback(
    (slotKey: string) => host.sessionAbandonCommand(slotKey, sessionId),
    [host, sessionId],
  );
  const onDismissOutgoing = useCallback(
    (reqId: string) => host.sessionDismissOutgoing(sessionId, reqId),
    [host, sessionId],
  );
  const onRestoreOutgoing = useCallback(
    (reqId: string) => void restoreOutgoingRef.current(reqId),
    [],
  );
  const onRestoreText = useCallback(
    async (text: string, reqId?: string) => {
      try {
        await remote.restoreDraft(sessionId, tray);
      } catch {
        return;
      }
      if (!mountedSession.current) return;
      const saved = reqId ? stash?.get(reqId) : undefined;
      if (saved?.items?.length) {
        const images = saved.items.filter((it) => it.kind !== "file" && it.imageId);
        if (
          images.length &&
          !tray.restore(
            images.map((it, i) => ({ imageId: it.imageId!, thumb: saved.thumbs?.[i], file: it.file })),
            !!saved.original,
          )
        ) {
          toastRef.current(
            i18n.t("composer.restore_images_full", { existing: tray.images().length, count: images.length }),
          );
          return;
        }
      }
      const cur = remote.drafts.get(sessionId) ?? "";
      remote.drafts.set(sessionId, cur.trim() ? cur.trimEnd() + "\n\n" + text : text);
      if (reqId) stash?.delete(reqId);
      setComposerKey((k) => k + 1);
      setTimeout(() => composerRef.current?.focus());
    },
    [remote, sessionId, tray, stash],
  );
  const onLoadEarlier = useCallback(() => {
    const tl = timelineRef.current;
    if (!tl) return;
    if (tl.hasGap || tl.canLoadEarlier) host.sessionLoadEarlier(sessionId);
    if (tl.hasEarlier) {
      setWindowLimit((n) => Math.max(n, tl.items.length) + DEFAULT_WINDOW_LIMIT);
    }
  }, [host, sessionId]);
  const onRetryEvents = useCallback(() => host.retryEvents(sessionId), [host, sessionId]);
  const onRefreshModels = useCallback(() => host.sessionOpen(sessionId, true, false), [host, sessionId]);
  const onExpandGroup = useCallback(
    (groupId: string) => setExpandedGroups((old) => (old.includes(groupId) ? old : [...old, groupId])),
    [],
  );
  const onScheduleRevealed = useCallback(() => host.clearScheduleFocus(sessionId), [host, sessionId]);

  return (
    <SessionScreenView
      vm={vm}
      wide={wide}
      onBack={handleBack}
      go={go}
      remote={remote}
      scheduleTaskCount={stripVm.visible ? stripVm.tasks.length : 0}
      draft={remote.drafts.get(sessionId) ?? ""}
      onDraft={(text) => {
        remote.drafts.set(sessionId, text);
        setDraftVer((n) => n + 1);
      }}
      tray={tray}
      trayView={<TrayView tray={tray} remote={remote} />}
      attach={<AttachButton tray={tray} blocked={attachWhy} onBlocked={setToast} />}
      cardProps={wide ? drop.props : { onPaste: drop.props.onPaste }}
      dropping={drop.over}
      composerRef={composerRef}
      composerKey={composerKey}
      approvalReason={approvalReason}
      onApprovalReasonChange={(id, v) => {
        if (v) remote.approvalReasons.set(id, v);
        else remote.approvalReasons.delete(id);
        remote.touch();
      }}
      localAttachments={stash}
      toast={toast}
      onClearToast={() => setToast("")}
      sentCount={sent}
      onSend={handleSend}
      onSubmitCommand={onSubmitCommand}
      onAbandonCommand={onAbandonCommand}
      onDismissOutgoing={onDismissOutgoing}
      onRestoreOutgoing={onRestoreOutgoing}
      onRestoreText={onRestoreText}
      onLoadEarlier={onLoadEarlier}
      onRetryEvents={onRetryEvents}
      onRefreshModels={onRefreshModels}
      onExpandGroup={onExpandGroup}
      onScheduleRevealed={onScheduleRevealed}
    />
  );
}

const ChatBody = memo(function ChatBody({
  vm,
  remote,
  sent,
  localAttachments,
  retainedReasons = [],
  defaultExpandedQueueAll,
  onRestoreText,
  onTerminal,
  onSubmitCommand,
  onRetryOutgoing,
  onDismissOutgoing,
  onRestoreOutgoing,
  onLoadEarlier,
  onRetryEvents,
  onExpandGroup,
  onScheduleRevealed,
}: {
  vm: SessionViewModel;
  remote?: Remote;
  sent: number;
  localAttachments?: Map<string, LocalSendAttachments>;
  retainedReasons?: Array<{ id: string; reason: string }>;
  defaultExpandedQueueAll?: boolean;
  onRestoreText?: (text: string, requestId?: string) => void;
  onTerminal?: () => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onRetryOutgoing: (slotKey: string, cmd: Command) => void;
  onDismissOutgoing: (requestId: string) => void;
  onRestoreOutgoing: (requestId: string) => void;
  onLoadEarlier: () => void;
  onRetryEvents?: () => void;
  onExpandGroup: (groupId: string) => void;
  onScheduleRevealed?: () => void;
}) {
  const sid = vm.sessionId;
  const header = vm.header;
  const timeline = vm.timeline;
  const composer = vm.composer;
  const dock = vm.dock;
  const locale = i18n.useLocale();
  useRemote(remote);

  const scroller = useRef<HTMLDivElement>(null);
  const getScrollStore = () => {
    if (remote?.scroll) return remote.scroll;
    const key = remote ?? LOCAL_SCROLL;
    let m = LOCAL_SCROLL.get(key);
    if (!m) {
      m = new Map();
      LOCAL_SCROLL.set(key, m);
    }
    return m;
  };
  const saved = useMemo(() => getScrollStore().get(sid), [sid]); // eslint-disable-line react-hooks/exhaustive-deps
  const needFullCache = !!(saved && !saved.stick);
  const stick = useRef(saved ? saved.stick : true);
  const lastScroll = useRef<Scrolled>(undefined);
  const userScrollAt = useRef(0);
  const [away, setAway] = useState(false);
  const [unseen, setUnseen] = useState(0);
  const rowStates = useRef(new Map<string, boolean>());
  const loadedTopAt = useRef(-1);

  const rawItems = timeline?.items ?? EMPTY_ITEMS;
  const rows = useMemo(() => flattenTimelineItems(rawItems), [rawItems]);
  const count = rows.length;
  const tailSeq = timeline?.windowLastSeq
    ? timeline.windowLastSeq
    : count > 0
      ? itemEndSeq(rows[count - 1].item)
      : 0n;
  const focusKey = timeline?.anchorItemId || undefined;

  const protocolNotice = header?.protocolMismatch ? renderText(header.protocolMismatchText) : "";
  const historyNotice = renderText(timeline?.historyNotice);
  const fetchError = renderText(timeline?.fetchError);
  const historyError = renderText(timeline?.error);
  const awaitingApprovalTool = !!(dock?.hasItem && dock.kind === SessionDockKind.APPROVAL);

  const readyRef = useRef<{ sid: string; ready: boolean }>({ sid, ready: false });
  const seenWaterSeq = useRef<bigint | undefined>(undefined);
  const seenKeys = useRef<Set<string>>(new Set());
  if (readyRef.current.sid !== sid) {
    readyRef.current = { sid, ready: false };
    seenWaterSeq.current = undefined;
    seenKeys.current.clear();
  }
  if (
    timeline?.retryingCorruptCache &&
    count === 0 &&
    !timeline?.loaded &&
    !historyError &&
    !timeline?.retired
  ) {
    readyRef.current.ready = false;
  }
  const canRevealTail =
    !timeline?.awaitingCache &&
    count > 0 &&
    (!needFullCache ||
      (!timeline?.backfilling &&
        !timeline?.fetching &&
        (!timeline?.hasGap || !!fetchError || !!timeline?.offline)));
  if (!!timeline?.loaded || !!historyError || !!timeline?.retired || canRevealTail) {
    readyRef.current.ready = true;
  }
  const ready = readyRef.current.ready;

  const markTailSeen = () => {
    seenWaterSeq.current = tailSeq;
    seenKeys.current.clear();
    for (const r of rows) recordRowSeen(seenKeys.current, r);
  };

  useEffect(() => {
    if (focusKey && onScheduleRevealed) onScheduleRevealed();
  }, [focusKey, onScheduleRevealed]);

  const knownRowKeys = useRef<Set<string> | undefined>(undefined);
  const newRowKeys = useRef(new Set<string>());
  if (ready) {
    if (knownRowKeys.current === undefined) {
      knownRowKeys.current = new Set(rows.map((r) => r.key));
    } else {
      newRowKeys.current.clear();
      const added: string[] = [];
      for (const r of rows) {
        if (!knownRowKeys.current.has(r.key)) added.push(r.key);
        knownRowKeys.current.add(r.key);
      }
      if (added.length <= 2) {
        for (const k of added) newRowKeys.current.add(k);
      }
    }
  }
  const freshRow = (k: string) => !!canAnimate() && newRowKeys.current.has(k);
  const rowClass = (it: TimelineRowEntry) => {
    if (!freshRow(it.key)) return undefined;
    const k = it.item.kind.case;
    if (k === "userMessage") return "mo-rise";
    if (k === "toolCall" || k === "toolGroup") return "mo-fade";
    return undefined;
  };

  const pressedAt = useRef(0);
  useEffect(() => {
    const el = scroller.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    let h = -1;
    const ro = new ResizeObserver((entries) => {
      let shrank = false;
      for (const e of entries) {
        if (e.target !== el) continue;
        const now = e.contentRect.height;
        if (h >= 0 && now < h) shrank = true;
        h = now;
      }
      lastScroll.current = { top: el.scrollTop, height: el.scrollHeight, client: el.clientHeight };
      setAway(!stick.current && el.scrollHeight - el.scrollTop - el.clientHeight >= NEAR_END);
      if (!stick.current) return;
      if (shrank || Date.now() - pressedAt.current > 800) el.scrollTop = el.scrollHeight;
    });
    ro.observe(el);
    const watched = new Set<Element>();
    const sync = () => {
      for (const c of el.children) if (!watched.has(c)) (watched.add(c), ro.observe(c));
      for (const c of watched) if (c.parentElement !== el) (watched.delete(c), ro.unobserve?.(c));
    };
    const mo = new MutationObserver(sync);
    mo.observe(el, { childList: true });
    sync();
    return () => {
      mo.disconnect();
      ro.disconnect();
    };
  }, []);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (ready && rows.length <= 100 && el && saved && !saved.stick) {
      el.scrollTop = saved.top;
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useLayoutEffect(() => {
    const el = scroller.current;
    if (!sent || !el) return;
    setAway(false);
    stick.current = true;
    el.scrollTop = el.scrollHeight;
    if (!focusKey) {
      markTailSeen();
      setUnseen(0);
    }
  }, [sent]); // eslint-disable-line react-hooks/exhaustive-deps

  const loadingEarlier = !!(timeline?.backfilling || (timeline?.tailRequested && timeline?.fetching));
  useEffect(() => {
    const el = scroller.current;
    if (!el || el.classList.contains("mo-hold") || stick.current) return;
    if (el.scrollTop <= 40 && !loadingEarlier && (timeline?.hasEarlier || timeline?.canLoadEarlier) && loadedTopAt.current !== count) {
      loadedTopAt.current = count;
      onLoadEarlier();
    }
  }, [loadingEarlier, timeline?.hasEarlier, timeline?.canLoadEarlier, count, onLoadEarlier]);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (focusKey) return;
    if (seenWaterSeq.current === undefined) {
      if (ready) markTailSeen();
      return;
    }
    if (stick.current) {
      if (rows.length <= 100) el.scrollTop = el.scrollHeight;
      markTailSeen();
      setUnseen(0);
      return;
    }
    setUnseen(countUnseenRows(rows, seenWaterSeq.current, seenKeys.current));
  }, [count, tailSeq, ready, focusKey, rawItems]); // eslint-disable-line react-hooks/exhaustive-deps

  const listHeader = (
    <>
      {(timeline?.hasEarlier || timeline?.canLoadEarlier || timeline?.backfilling || timeline?.tailRequested) && (
        <div className="load-earlier">
          {loadingEarlier ? (
            <div className="muted small">{i18n.t("session.loading_earlier")}</div>
          ) : (
            <button className="more-row" onClick={onLoadEarlier}>
              {i18n.t("schedule.earlier")}
            </button>
          )}
        </div>
      )}
      {historyNotice && <div className="sysline">{historyNotice}</div>}
      {protocolNotice && <Banner kind="warn">{protocolNotice}</Banner>}
    </>
  );

  const listFooter = (
    <>
      {fetchError && (
        <Banner kind="err" action={onRetryEvents ? t("common.retry") : undefined} onAction={onRetryEvents}>
          {fetchError}
        </Banner>
      )}
      {header?.status === SessionDisplayStatus.STARTING && (
        <div className="sysline">
          {i18n.t("session.starting_agent", { name: renderText(header.computerName), agent: header.agentName })}
        </div>
      )}
      {retainedReasons.map(({ id, reason }) => (
        <div className="record" key={"reason:" + id}>
          <span className="muted">{i18n.t("session.approval_resolved")}</span>
          <pre>{reason}</pre>
          <button
            className="link"
            onClick={() => void navigator.clipboard.writeText(reason).catch(() => undefined)}
          >
            {i18n.t("common.copy")}
          </button>
        </div>
      ))}
      <OutgoingBubbles
        sessionId={sid}
        outgoing={composer?.outgoing ?? []}
        localAttachments={localAttachments}
        onRetry={onRetryOutgoing}
        onRestore={onRestoreOutgoing}
        onDismiss={onDismissOutgoing}
      />
      <PendingBubbles
        sessionId={sid}
        pendingInputs={composer?.pendingInputs ?? []}
        localAttachments={localAttachments}
        defaultExpandedAll={defaultExpandedQueueAll}
        onSubmitCommand={onSubmitCommand}
      />
    </>
  );

  const online = header?.computerOnline ?? true;
  const mname = renderText(header?.computerName);
  const hasSharedFiles = header?.capabilities?.supportsSharedFiles ?? false;

  const renderEntry = useCallback(
    (entry: TimelineRowEntry) => (
      <RowState id={entry.key} states={rowStates.current}>
        <TimelineRow
          item={entry.item}
          sessionId={sid}
          online={online}
          lostAt={remote?.lostAt}
          awaitingApproval={awaitingApprovalTool}
          remote={remote}
          machineName={mname}
          hasSharedFiles={hasSharedFiles}
          locale={locale}
          onTerminal={onTerminal}
          localAttachments={localAttachments}
          onRestoreText={onRestoreText}
          onExpandGroup={onExpandGroup}
        />
      </RowState>
    ),
    [sid, online, remote, remote?.lostAt, awaitingApprovalTool, mname, hasSharedFiles, locale, onTerminal, localAttachments, onRestoreText, onExpandGroup],
  );

  return (
    <div className="chatwrap">
      <div
        className="body chat"
        ref={scroller}
        onPointerDownCapture={() => {
          pressedAt.current = Date.now();
        }}
        onKeyDownCapture={() => {
          pressedAt.current = Date.now();
        }}
        onWheel={() => {
          userScrollAt.current = Date.now();
        }}
        onTouchMove={() => {
          userScrollAt.current = Date.now();
        }}
        onKeyDown={() => {
          userScrollAt.current = Date.now();
        }}
        onPointerDown={(e) => {
          if (e.target === e.currentTarget) userScrollAt.current = Date.now();
        }}
        onPointerMove={(e) => {
          if (e.buttons) userScrollAt.current = Date.now();
        }}
        onScroll={(e) => {
          const el = e.currentTarget;
          if (el.classList.contains("mo-hold")) return;
          const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
          const now = { top: el.scrollTop, height: el.scrollHeight, client: el.clientHeight };
          stick.current = followsEnd(stick.current, lastScroll.current, now, userScrollAt.current, Date.now());
          lastScroll.current = now;
          setAway(!stick.current && gap >= NEAR_END);
          if (stick.current && gap > 0 && gap < 40 && Date.now() - userScrollAt.current >= 1000) {
            el.scrollTop = el.scrollHeight;
          }
          getScrollStore().set(sid, { top: el.scrollTop, stick: stick.current });
          if (stick.current && !focusKey) {
            markTailSeen();
            setUnseen(0);
          }
          if (el.scrollTop <= 40 && !loadingEarlier && (timeline?.hasEarlier || timeline?.canLoadEarlier) && loadedTopAt.current !== rows.length) {
            loadedTopAt.current = rows.length;
            onLoadEarlier();
          }
        }}
      >
        {remote ? (
          <HistoryRows
            items={rows}
            focusKey={focusKey}
            scroller={scroller}
            remote={remote}
            sid={sid}
            follow={() => stick.current}
            rowClass={rowClass}
            long={!!timeline?.hasEarlier || !!timeline?.backfilling || !!timeline?.tailRequested || rows.length > 100}
            loaded={ready}
            ready={ready}
            header={listHeader}
            footer={listFooter}
            render={renderEntry}
          />
        ) : (
          <>
            {listHeader}
            {rows.map((entry, index) => (
              <div key={entry.key} className={"history-row" + (rowClass(entry) ? " " + rowClass(entry) : "")} data-history-index={index + 1}>
                {renderEntry(entry)}
              </div>
            ))}
            {listFooter}
          </>
        )}
      </div>
      {remote && (
        <HistoryOpening
          waiting={!ready && rows.length === 0}
          connected={remote.conn === "online"}
          scroller={scroller}
          error={historyError || undefined}
          onRetry={onRetryEvents}
        />
      )}
      {(away || unseen > 0) && (
        <NewMessages
          n={unseen}
          onClick={() => {
            const el = scroller.current;
            userScrollAt.current = 0;
            setAway(false);
            if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
            stick.current = true;
            markTailSeen();
            setUnseen(0);
          }}
        />
      )}
    </div>
  );
});

function NewMessages({ n, onClick }: { n: number; onClick: () => void }) {
  const ref = useRef<HTMLButtonElement>(null);
  useExit(ref);
  return (
    <button ref={ref} className="newmsg" onClick={onClick}>
      <ArrowDown /> {n > 0 ? i18n.t("session.new_messages", { count: n }) : i18n.t("session.latest")}
    </button>
  );
}

const TimelineRow = memo(function TimelineRow({
  item,
  sessionId,
  online,
  lostAt,
  awaitingApproval,
  remote,
  machineName,
  hasSharedFiles,
  locale: _locale,
  localAttachments,
  onTerminal,
  onRestoreText,
  onExpandGroup,
}: {
  item: SessionTimelineItem;
  sessionId: string;
  online: boolean;
  lostAt?: number;
  awaitingApproval: boolean;
  remote?: Remote;
  machineName: string;
  hasSharedFiles: boolean;
  locale: string;
  localAttachments?: Map<string, LocalSendAttachments>;
  onTerminal?: () => void;
  onRestoreText?: (text: string, requestId?: string) => void;
  onExpandGroup: (groupId: string) => void;
}) {
  switch (item.kind.case) {
    case "userMessage": {
      const m = item.kind.value;
      const files = m.files.map((f, i) => ({
        fileId: f.path || f.name || String(i),
        name: f.name,
        sizeBytes: f.sizeBytes,
        path: f.path,
      }));
      const segs = splitPlaceholders(withoutFileLines(m.text, files), m.images);
      const caption = renderText(m.originCaption);
      return (
        <div className="me">
          <ImageStrip refs={m.images} numbered />
          <FileChips files={files} />
          {m.isScheduled ? (
            <ScheduleText key={item.id} text={m.text} meta={caption} />
          ) : (
            segs.length > 0 && (
              <div className="mo-text">
                {segs.map((x, i) =>
                  "text" in x ? (
                    <span key={i}>{x.text}</span>
                  ) : (
                    <span key={i} className="ichip">
                      {i18n.t("image.placeholder", { index: x.image })}
                    </span>
                  ),
                )}
              </div>
            )
          )}
          {!m.isScheduled && caption && <small>{caption}</small>}
        </div>
      );
    }
    case "agentMessage": {
      const a = item.kind.value;
      return (
        <div className={"ai" + (a.intermediate ? " commentary" : "")}>
          <Markdown text={a.markdown} />
        </div>
      );
    }
    case "reasoning": {
      const r = item.kind.value;
      return (
        <RememberDetails name="details" className="think">
          <summary>
            {i18n.t("session.thinking")}
            <Exp />
          </summary>
          <div className="thinktext">{r.markdown}</div>
        </RememberDetails>
      );
    }
    case "toolCall":
      return (
        <ToolCallView
          item={item.kind.value}
          online={online}
          lostAt={lostAt}
          awaitingApproval={awaitingApproval}
        />
      );
    case "toolGroup":
      return <ToolGroupView group={item.kind.value} />;
    case "sharedFiles": {
      const sf = item.kind.value;
      if (hasSharedFiles && remote) {
        const files = sf.files.map((f) =>
          create(SharedFileSchema, {
            path: f.path,
            name: f.name,
            sizeBytes: f.sizeBytes,
            fileId: f.fileId,
          }),
        );
        return (
          <SharedFileCards
            remote={remote}
            sessionId={sessionId}
            files={files}
            machineName={machineName}
          />
        );
      }
      return (
        <FileChips
          files={sf.files.map((f, i) => ({
            fileId: f.fileId || f.path || f.name || String(i),
            name: f.name,
            sizeBytes: f.sizeBytes,
          }))}
        />
      );
    }
    case "generatedImages":
      return <GenCard refs={item.kind.value.images} prompt={item.kind.value.revisedPrompt} />;
    case "asyncQuestion": {
      const aq = item.kind.value;
      return (
        <div className="ai">
          {aq.questions.map((q, i) => (
            <div key={i}>
              {q.header && <b>{q.header}</b>}
              <Markdown text={q.questionText} />
              {q.options.length > 0 && (
                <ul>
                  {q.options.map((o, j) => (
                    <li key={j}>
                      <b>{o.label}</b>
                      {o.description && ` · ${o.description}`}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          ))}
        </div>
      );
    }
    case "plan":
      return (
        <RememberDetails name="details" className="think">
          <summary>
            {i18n.t("modes.plan")}
            <Exp />
          </summary>
          <div className="ai">
            <Markdown text={item.kind.value.markdown} />
          </div>
        </RememberDetails>
      );
    case "note": {
      const n = item.kind.value;
      if (n.kind === SessionNoteKind.APPROVAL && n.meta) {
        const ok = n.tone === SessionNoteTone.POSITIVE;
        const bad = n.tone === SessionNoteTone.NEGATIVE;
        const canceled = !ok && !bad;
        return (
          <div className="record">
            <span className={canceled ? "muted" : ok ? "ok" : "x"}>
              {canceled ? <Minus /> : ok ? <Check /> : <X />}
            </span>
            <code>{renderText(n.meta)}</code>
            <span className="muted">{renderText(n.text)}</span>
          </div>
        );
      }
      const cls = n.tone === SessionNoteTone.NEGATIVE ? " fail" : n.tone === SessionNoteTone.WARNING ? " warn" : "";
      return <div className={"sysline" + cls}>{renderText(n.text)}</div>;
    }
    case "turnEnd": {
      const te = item.kind.value;
      if (te.interrupted) return <div className="sysline">{renderText(te.label)}</div>;
      return <div className="sep">{renderText(te.label)}</div>;
    }
    case "errorLine":
      return <div className="sysline fail">{renderText(item.kind.value.text) || item.kind.value.rawReason}</div>;
    case "forkMarker": {
      const f = item.kind.value;
      const text = renderText(f.text);
      return (
        <div className="sep fork">
          <span>
            {f.sourceSessionId ? (
              <a className="src" href={"#/s/" + encodeURIComponent(f.sourceSessionId)}>
                {text}
                <ChevronRight />
              </a>
            ) : (
              text
            )}
          </span>
        </div>
      );
    }
    case "inputState": {
      const s = item.kind.value;
      const status = renderText(s.statusText);
      const reason = renderText(s.reasonText);
      const att = s.requestId ? localAttachments?.get(s.requestId) : undefined;
      if (s.kind === SessionInputNoticeKind.CANCELED) {
        return <div className="sysline">{i18n.t("queue.canceled_record")}</div>;
      }
      if (s.kind === SessionInputNoticeKind.UNSENT_ON_EXIT) {
        return s.preview ? (
          <div className="me fail unsent">
            <div>{s.preview}</div>
            <small>
              {status || i18n.t("session.ended_send_failed")}
              {onRestoreText && <button onClick={() => onRestoreText(s.preview, s.requestId)}>{i18n.t("composer.restore")}</button>}
            </small>
          </div>
        ) : (
          <div className="sysline fail">{status || i18n.t("session.ended_message_failed")}</div>
        );
      }
      if (s.kind === SessionInputNoticeKind.NEEDS_TERMINAL) {
        return (
          <div className="me warn">
            <LocalThumbs blobs={att?.thumbs} sizes={att?.imageSizes} />
            <div>{s.preview || i18n.t("composer.message_fallback")}</div>
            <small>
              {reason || status}
              {onTerminal && <button onClick={onTerminal}>{i18n.t("session.go_terminal")}</button>}
              {s.preview && onRestoreText && <button onClick={() => onRestoreText(s.preview, s.requestId)}>{i18n.t("composer.restore")}</button>}
            </small>
          </div>
        );
      }
      if (s.kind === SessionInputNoticeKind.FAILED) {
        return (
          <div className="me fail">
            <LocalThumbs blobs={att?.thumbs} sizes={att?.imageSizes} />
            <div>{s.preview || i18n.t("composer.message_fallback")}</div>
            <small>
              {reason || status}
              {s.preview && onRestoreText && <button onClick={() => onRestoreText(s.preview, s.requestId)}>{i18n.t("composer.restore")}</button>}
            </small>
          </div>
        );
      }
      return s.preview ? (
        <div className="me warn">
          <div>{s.preview}</div>
          <small>{status || i18n.t("composer.unconfirmed")}</small>
        </div>
      ) : (
        <div className="sysline warn">{status || i18n.t("composer.message_unconfirmed")}</div>
      );
    }
    case "quietScheduledGroup": {
      const g = item.kind.value;
      return (
        <button className="schedule-quiet" onClick={() => onExpandGroup(g.groupId)}>
          {renderText(g.title)} <ChevronDown />
        </button>
      );
    }
    case "skeleton":
      return <div className="record muted">{renderText(item.kind.value.text) || skeletonMessage()}</div>;
    default:
      return null;
  }
});

function EndCard({
  sessionId,
  header,
  actions,
  composer,
  onSubmitCommand,
}: {
  sessionId: string;
  header?: SessionHeaderView;
  actions?: SessionActionsView;
  composer?: SessionViewModel["composer"];
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
}) {
  const ended = !!header?.exited;
  const mname = renderText(header?.computerName);
  const resumeCmd = actions?.resume;
  const resumeError = commandErrorText(resumeCmd);

  if (!ended) {
    return (
      <div className="endcard">
        <div className="h">
          <StateMark kind="off" />
          {renderText(composer?.disabledReason) || i18n.t("new_session.machine_offline", { name: mname })}
        </div>
        {header?.offlineText && <div className="d">{renderText(header.offlineText)}</div>}
      </div>
    );
  }

  const doResume = () =>
    onSubmitCommand(
      resumeCmd?.slotKey ?? "",
      create(CommandSchema, {
        kind: { case: "resumeSession", value: { sessionId } },
      }),
    );

  if (resumeCmd?.busy) {
    return (
      <div className="endcard">
        <div className="h">
          <StateMark kind="run" />
          {i18n.t("session.resuming")}
        </div>
        <div className="d">{i18n.t("session.reopen_agent", { name: mname, agent: header?.agentName ?? "" })}</div>
      </div>
    );
  }

  if (resumeError) {
    return (
      <div className="endcard">
        <div className="h">
          <StateMark kind="off" />
          {i18n.t("session.resume_failed")}
        </div>
        <div className="d">{resumeError}</div>
        <div className="btns">
          <button className="btn" onClick={doResume} disabled={!actions?.canResume}>
            {i18n.t("session.try_again")}
          </button>
        </div>
      </div>
    );
  }

  let resumeBody: ReactNode;
  if (header?.computerUnlinked) {
    resumeBody = <div className="d">{i18n.t("session.connect_resume")}</div>;
  } else if (header?.computerOnline || header?.computerReachability === ComputerReachability.UNKNOWN) {
    resumeBody = (
      <>
        <div className="d">
          {renderText(header.endedText) || renderText(actions?.resumeNote) || i18n.t("session.resume_note", { name: mname })}
        </div>
        {header?.computerOnline && (
          <div className="btns">
            <button className="btn pri" onClick={doResume} disabled={!actions?.canResume}>
              {i18n.t("session.resume")}
            </button>
          </div>
        )}
      </>
    );
  } else {
    resumeBody = <div className="d">{renderText(actions?.resumeNote) || i18n.t("session.offline_resume", { name: mname })}</div>;
  }

  return (
    <div className="endcard">
      <div className="h">
        <StateMark kind="off" />
        {i18n.t("session.ended")}
      </div>
      {resumeBody}
    </div>
  );
}
