import * as i18n from "../i18n";
import { t } from "../i18n";
import { AgentMark } from "../agent-mark";
import { routeForTask, routeForUsage, useCoreHost, useScheduleSessionStripView, useSessionView, type Go, type SessionWindowOptions } from "../core/host";
import { renderText } from "../core/text";
import { newRequestID } from "../crypto";
import {
  ComputerReachability,
  SessionCommandStatus,
  SessionDockKind,
  SessionInputNoticeKind,
  SessionNoteKind,
  SessionNoteTone,
  SharedFileWebPreviewKind,
  type SessionActionsView,
  type SessionCommandState,
  type SessionHeaderView,
  type SessionNotifyReceiptItem,
  type SessionResendAttachmentsEffect,
  type SessionRestoreComposerEffect,
  type SessionSharedFileItem,
  type SessionTimelineItem,
  type SessionViewerImageView,
  type SessionViewModel,
} from "../gen/remoteagent/core/v1/session_view_pb";
import { ConnState, SessionDisplayStatus, SessionTone } from "../gen/remoteagent/core/v1/home_view_pb";
import { Agent, ApprovalMode, SendMode, type ImageRef } from "../gen/remoteagent/v1/event_pb";
import {
  CommandSchema,
  type Command,
} from "../gen/remoteagent/v1/remote_pb";
import {
  ArrowDown,
  Bell,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Ellipsis,
  Minus,
  X,
  Zap,
} from "../icons";
import { Markdown } from "../markdown";
import { canAnimate, easeBetween, useExit, useKnob } from "../motion";
import { type Remote } from "../remote";
import { saveBlobAsDownload, sharedFilesOf } from "../shared-files";
import { ModeSheet, RenameDialog, SessionSheet } from "../sheets";
import { ActionSheet, Alert, Banner, ConnBar, Exp, NavBar, PathText, Sheet, StateMark, useRemote, type Mark } from "../ui";
import { getView, setView, type View } from "../view";
import { AttachButton, TrayView, useDropPaste, useTray } from "./Attach";
import { Composer } from "./Composer";
import { ApprovalPage, Dock, PlanPage, QuestionPage, type QuestionDraftState } from "./Dock";
import { FileChip, FileChips } from "./Files";
import { HistoryOpening } from "./HistoryOpening";
import { HistoryRows } from "./HistoryRows";
import {
  GenCard,
  ImageStrip,
  ImageViewer,
  LocalThumbs,
  OpenImage,
  ViewerImagesMeta,
  viewerImageKey,
  type ImageAnchor,
} from "./Images";
import { PendingTray, type LocalSendAttachments } from "./Queue";
import { RememberDetails, RowState, useRowExpanded } from "./RowState";
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
  defaultDenying?: boolean;
  defaultExpandedQueueAll?: boolean;
  defaultKeyboardCollapsed?: boolean;
  onSend: (text: string, mode: SendMode) => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onAbandonCommand: (slotKey: string) => void;
  onCancelTrayItem?: (requestId: string) => void;
  onIgnoreTrayItem?: (requestId: string) => void;
  onPromoteTrayItem?: (requestId: string) => void;
  onRestoreTrayItem?: (requestId: string) => void;
  onRetryTrayItem?: (requestId: string) => void;
  onResumeTrayItem?: (requestId: string) => void;
  onCancelAllPaused?: () => void;
  onUndoCancel?: () => void;
  onRestoreText?: (text: string, requestId?: string) => void;
  onLoadEarlier: () => void;
  onRetrySession?: () => void;
  onRetryEvents?: () => void;
  onRefreshModels?: () => void;
  onExpandGroup: (groupId: string) => void;
  onScheduleRevealed?: () => void;
  onOpenImageViewer?: (anchor: ImageAnchor) => void;
  onNavigateImageViewer?: (delta: number) => void;
  onCloseImageViewer?: () => void;
}

const noopStr = (_id: string) => undefined;
const noop = () => undefined;

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
  defaultDenying = false,
  defaultExpandedQueueAll = false,
  defaultKeyboardCollapsed = false,
  onSend,
  onSubmitCommand,
  onAbandonCommand,
  onCancelTrayItem = noopStr,
  onIgnoreTrayItem = noopStr,
  onPromoteTrayItem = noopStr,
  onRestoreTrayItem = noopStr,
  onRetryTrayItem = noopStr,
  onResumeTrayItem = noopStr,
  onCancelAllPaused = noop,
  onUndoCancel = noop,
  onRestoreText,
  onLoadEarlier,
  onRetrySession,
  onRetryEvents,
  onRefreshModels,
  onExpandGroup,
  onScheduleRevealed,
  onOpenImageViewer,
  onNavigateImageViewer,
  onCloseImageViewer,
}: SessionScreenViewProps) {
  const sid = vm.sessionId;
  const header = vm.header;
  const actions = vm.actions;
  const dock = vm.dock;
  const composer = vm.composer;
  const timeline = vm.timeline;

  const [rootRef, width] = useWidth<HTMLDivElement>();
  const [colRef, colHeight] = useHeight();
  const colNodeRef = useRef<HTMLDivElement | null>(null);
  const setColRef = useCallback(
    (el: HTMLDivElement | null) => {
      colNodeRef.current = el;
      colRef(el);
    },
    [colRef],
  );
  const room = colHeight > 0 ? colHeight - 230 : Infinity;
  const canSplit = wide && width >= 840;

  const hasContent =
    (vm.timeline?.items.length ?? 0) > 0 ||
    (vm.composer?.pendingTray?.totalCount ?? 0) > 0 ||
    !!vm.composer?.pendingTray?.undo?.visible;
  const pending = (!header || (!header.found && !header.missing && !header.lookupFailed)) && !hasContent;
  const exited = !!header?.exited;
  const reachabilityUnknown = (() => {
    return header?.computerReachability === ComputerReachability.UNKNOWN;
  })();
  const unlinked = !!header?.computerUnlinked && !reachabilityUnknown;
  const showEndCard = exited || unlinked;
  const inactive = exited || unlinked || (!!header?.found && !header.computerOnline && !reachabilityUnknown);

  // Mobile soft-keyboard tray collapse and 200ms FLIP row-to-bubble transition.
  const [composerFocused, setComposerFocused] = useState(false);
  const [kbCollapsed, setKbCollapsed] = useState(false);
  useEffect(() => {
    if (wide) {
      setKbCollapsed(false);
      return;
    }
    const vv = typeof window !== "undefined" ? window.visualViewport : null;
    if (!vv) return;
    const syncKb = () => {
      const drop = window.innerHeight - vv.height;
      setKbCollapsed(composerFocused && drop > 120);
    };
    syncKb();
    vv.addEventListener("resize", syncKb);
    return () => vv.removeEventListener("resize", syncKb);
  }, [wide, composerFocused]);

  const trayRowRefs = useRef(new Map<string, HTMLDivElement>());
  const prevTraySnapRef = useRef<Array<{ id: string; text: string; top: number; left: number }>>([]);
  const prevUserIDsRef = useRef<{ sid: string; ids: Set<string> }>({ sid: "", ids: new Set() });
  const prevTrayHeightRef = useRef<{ sid: string; h: number }>({ sid: "", h: 0 });
  const pendingTray = composer?.pendingTray;

  useLayoutEffect(() => {
    const items = timeline?.items ?? [];
    const curUserIDs = new Set<string>();
    const newUserMsgs: Array<{ id: string; reqId: string; text: string }> = [];
    const prevSet = prevUserIDsRef.current.sid === sid ? prevUserIDsRef.current.ids : null;
    for (const it of items) {
      if (it.kind.case === "userMessage") {
        curUserIDs.add(it.id);
        if (prevSet && !prevSet.has(it.id)) {
          newUserMsgs.push({
            id: it.id,
            reqId: it.kind.value.origin?.requestId ?? "",
            text: it.kind.value.text.trim(),
          });
        }
      }
    }

    const activeTrayIDs = new Set((pendingTray?.orderedItems ?? []).map((x) => x.requestId));
    const colEl = colNodeRef.current;
    const reduceMotion =
      !canAnimate() ||
      (typeof window !== "undefined" && !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches);

    if (prevSet && colEl && !reduceMotion && newUserMsgs.length > 0 && prevTraySnapRef.current.length > 0) {
      for (const msg of newUserMsgs) {
        const snap = prevTraySnapRef.current.find(
          (s) =>
            !activeTrayIDs.has(s.id) &&
            ((msg.reqId && s.id === msg.reqId) || (!msg.reqId && msg.text && s.text === msg.text)),
        );
        if (!snap) continue;
        const msgEl =
          colEl.querySelector<HTMLElement>(`[data-msg-id="${CSS.escape(msg.id)}"] .mo-text`) ??
          colEl.querySelector<HTMLElement>(`[data-msg-id="${CSS.escape(msg.id)}"]`);
        if (!msgEl || typeof msgEl.animate !== "function") continue;
        const to = msgEl.getBoundingClientRect();
        if (to.height <= 0 && to.width <= 0) continue;
        const dx = snap.left - to.left;
        const dy = snap.top - to.top;
        if (Math.abs(dx) > 1 || Math.abs(dy) > 1) {
          msgEl.animate(
            [
              { transform: `translate(${dx}px, ${dy}px) scale(.96)`, opacity: 0.72 },
              { transform: "none", opacity: 1 },
            ],
            { duration: 200, easing: "cubic-bezier(.2, .8, .2, 1)" },
          );
        }
      }
    }

    const ptrayEl = colEl?.querySelector<HTMLElement>(".ptray");
    const curTrayH = ptrayEl ? ptrayEl.getBoundingClientRect().height : 0;
    if (ptrayEl && prevTrayHeightRef.current.sid === sid && prevTrayHeightRef.current.h > 0 && curTrayH > 0 && !reduceMotion) {
      easeBetween(ptrayEl, prevTrayHeightRef.current.h, curTrayH);
    }
    prevTrayHeightRef.current = { sid, h: curTrayH };

    prevUserIDsRef.current = { sid, ids: curUserIDs };
    const nextSnap: Array<{ id: string; text: string; top: number; left: number }> = [];
    for (const it of pendingTray?.orderedItems ?? []) {
      const el = trayRowRefs.current.get(it.requestId);
      if (!el) continue;
      const r = el.getBoundingClientRect();
      nextSnap.push({ id: it.requestId, text: it.text.trim(), top: r.top, left: r.left });
    }
    prevTraySnapRef.current = nextSnap;
  }, [sid, timeline?.items, pendingTray?.orderedItems, colRef]);
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

  const chip = header?.directChip;
  const directActive =
    !exited &&
    !!header?.computerOnline &&
    !header?.computerUnlinked &&
    !!chip?.active;
  const showDirectChip = (!exited && !!chip?.visible) || directOpen;

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

  const [viewingShared, setViewingShared] = useState<SharedFilePreviewTarget | undefined>(undefined);
  const items = timeline?.items ?? EMPTY_ITEMS;
  const viewerImages = timeline?.viewerImages;
  const viewerMetaByKey = useMemo(() => {
    const map = new Map<string, SessionViewerImageView>();
    for (const v of viewerImages ?? []) {
      map.set(viewerImageKey(v.seq, v.eventImageIndex), v);
    }
    return map;
  }, [viewerImages]);
  const previewRefsByKey = useMemo(() => {
    const map = new Map<string, ImageRef>();
    const visit = (list: readonly SessionTimelineItem[]) => {
      for (const item of list) {
        switch (item.kind.case) {
          case "userMessage":
            item.kind.value.images.forEach((ref, i) => map.set(viewerImageKey(item.seq, i), ref));
            break;
          case "toolCall": {
            const seq = item.kind.value.seq || item.seq;
            item.kind.value.images.forEach((ref, i) => map.set(viewerImageKey(seq, i), ref));
            break;
          }
          case "toolGroup":
            for (const call of item.kind.value.calls) {
              const seq = call.seq || item.seq;
              call.images.forEach((ref, i) => map.set(viewerImageKey(seq, i), ref));
            }
            break;
          case "generatedImages":
            item.kind.value.images.forEach((ref, i) => map.set(viewerImageKey(item.seq, i), ref));
            break;
          case "quietScheduledGroup":
            visit(item.kind.value.items);
            break;
        }
      }
    };
    visit(items);
    return map;
  }, [items]);
  const openImage = useCallback(
    (anchor: ImageAnchor) => {
      onOpenImageViewer?.(anchor);
    },
    [onOpenImageViewer],
  );
  const imageViewer = vm.imageViewer;
  const currentViewerPreview = imageViewer?.currentImage
    ? previewRefsByKey.get(viewerImageKey(imageViewer.currentImage.seq, imageViewer.currentImage.eventImageIndex))
    : undefined;

  const openSharedFile = useCallback((file: SessionSharedFileItem, blob: Blob) => {
    if (
      file.webPreviewKind === SharedFileWebPreviewKind.UNSPECIFIED ||
      !file.webPreviewMime
    ) {
      saveBlobAsDownload(blob, file.displayName);
      return;
    }
    setViewingShared({
      file,
      blob,
      kind: file.webPreviewKind,
      previewBlob: new Blob([blob], { type: file.webPreviewMime }),
    });
  }, []);

  const sharedItemsById = useMemo(() => {
    const map = new Map<string, SessionSharedFileItem>();
    for (const item of items) {
      if (item.kind.case === "sharedFiles") {
        for (const f of item.kind.value.files) map.set(f.fileId, f);
      }
    }
    return map;
  }, [items]);
  const sharedItemsRef = useRef(sharedItemsById);
  sharedItemsRef.current = sharedItemsById;

  useEffect(() => {
    if (!remote) return;
    const files = sharedFilesOf(remote);
    files.setCurrentSession(sid);
    files.setAutoOpenHandler((targetSid, file, blob) => {
      if (targetSid !== sid) return;
      const item = sharedItemsRef.current.get(file.fileId);
      if (item) openSharedFile(item, blob);
    });
    return () => {
      files.setCurrentSession(undefined);
      files.setAutoOpenHandler(undefined);
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
  const lookupFailed = !header?.found && !!header?.lookupFailed && !hasContent;
  if (lookupFailed) {
    return (
      <div className="page">
        <NavBar onBack={onBack} />
        <div className="center">
          <div>
            <div className="et">{renderText(header?.lookupErrorText) || renderText(header?.statusText) || i18n.t("session.ios_missing")}</div>
            <div className="big">
              {onRetrySession && (
                <button className="btn pri" onClick={onRetrySession}>
                  {i18n.t("common.retry")}
                </button>
              )}
              <button className="btn" onClick={onBack}>
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
        <span>{renderText(chip?.labelText) || t("transport.direct")}</span>
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
  const high = !!header?.highContextWarning;
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
      {header?.taskOwned && (
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
  const forkUi = actions?.forkAction;

  const chatCol = (
    <div className="ccol" ref={setColRef}>
      <ChatBody
        vm={vm}
        remote={remote}
        sent={sentCount}
        localAttachments={localAttachments}
        retainedReasons={retainedReasons}
        onRestoreText={onRestoreText}
        onTerminal={exited ? undefined : toTerminal}
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
      {!showEndCard && (
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
      {header?.taskOwned && <div className="schedule-reply-hint">{i18n.t("session.reply_moves")}</div>}
      {showEndCard ? (
        <EndCard
          sessionId={sid}
          header={header}
          actions={actions}
          composer={composer}
          onSubmitCommand={onSubmitCommand}
        />
      ) : null}
      {!showEndCard && !dockDenying && (
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
          pendingTray={
            <PendingTray
              tray={composer?.pendingTray}
              keyboardCollapsed={defaultKeyboardCollapsed || kbCollapsed}
              onTerminal={exited ? undefined : toTerminal}
              onCancelItem={onCancelTrayItem}
              onIgnoreItem={onIgnoreTrayItem}
              onPromoteItem={onPromoteTrayItem}
              onRestoreItem={onRestoreTrayItem}
              onRetryItem={onRetryTrayItem}
              onResumeItem={onResumeTrayItem}
              onCancelAllPaused={onCancelAllPaused}
              onUndo={onUndoCancel}
              rowRefs={trayRowRefs}
              defaultExpandedAll={defaultExpandedQueueAll}
            />
          }
          attach={attach}
          cardProps={cardProps}
          dropping={dropping}
          interruptDisabled={!!interruptCmd?.busy}
          demoted={dockOpen}
          wide={wide}
          onFocusChange={setComposerFocused}
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
    ...(forkUi?.visible
      ? [
          forkUi.busy
            ? {
                label: renderText(forkUi.label) || i18n.t("session.forking"),
                busy: true,
                keep: true,
                group: 1,
                onClick: () => undefined,
              }
            : {
                label: renderText(forkUi.label) || i18n.t("session.fork"),
                disabled: !forkUi.enabled,
                keep: forkUi.enabled,
                note: renderText(forkUi.note) || undefined,
                noteRed: forkUi.noteError,
                group: 1,
                onClick: () => {
                  if (!forkUi.enabled) return;
                  if (forkUi.abandons) {
                    if (forkCmd?.slotKey) onAbandonCommand(forkCmd.slotKey);
                  } else {
                    forkAction();
                  }
                },
              },
          ...(forkUi.canAbandon && forkCmd?.slotKey
            ? [
                {
                  label: i18n.t("session.abandon_fork"),
                  keep: true,
                  group: 1,
                  onClick: () => onAbandonCommand(forkCmd.slotKey),
                },
              ]
            : []),
        ]
      : []),
    ...(remote && header
      ? [
          {
            label: i18n.t("session.schedule_send"),
            group: 1,
            disabled: !header?.capabilities?.supportsSchedule || !header?.computerOnline || !!header?.computerUnlinked,
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
      <ViewerImagesMeta.Provider value={viewerMetaByKey}>
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
                    <PathText path={header?.shortPath ?? ""} />
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
              onOpenUsage={
                header?.machineId && go
                  ? () => {
                      setSheet("");
                      go(routeForUsage(header.machineId, header.agent, header.model));
                    }
                  : undefined
              }
            />
          )}
          {imageViewer?.open && (
            <ImageViewer
              remote={remote}
              sid={sid}
              viewer={imageViewer}
              previewRef={currentViewerPreview}
              title={title}
              wide={wide}
              onNavigate={(delta) => onNavigateImageViewer?.(delta)}
              onClose={() => onCloseImageViewer?.()}
            />
          )}
          {viewingShared && remote && (
            <SharedFilePreviewOverlay
              remote={remote}
              sessionId={sid}
              wide={wide}
              target={viewingShared}
              onClose={() => setViewingShared(undefined)}
            />
          )}
          {directOpen && header && typeof document !== "undefined" && createPortal(
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
                    {renderText(chip?.popoverTitle) ||
                      (directActive ? t("transport.popover_direct_title", { name: mname }) : t("transport.popover_relay_title", { name: mname }))}
                  </span>
                </div>
                <p className="dpop-body">
                  {renderText(chip?.popoverBody) ||
                    (directActive ? t("transport.popover_direct_body") : t("transport.popover_relay_body"))}
                </p>
                <div className="dpop-foot">
                  <span>
                    {renderText(chip?.popoverFoot) || t("transport.popover_switched_relay")}
                  </span>
                  {go && (
                    <button
                      type="button"
                      onClick={() => {
                        setDirectOpen(false);
                        go(`m/${encodeURIComponent(header.machineId)}/direct`);
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
      </ViewerImagesMeta.Provider>
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
  const caps = header?.capabilities;

  const [toast, setToast] = useState("");
  const toastRef = useRef(setToast);
  toastRef.current = setToast;
  const [sent, setSent] = useState(0);
  const [, setDraftVer] = useState(0);
  const [composerKey, setComposerKey] = useState(0);
  const composerRef = useRef<HTMLTextAreaElement>(null);

  const attachWhy = renderText(caps?.imageUploadBlockedReason);
  const fileWhy = renderText(caps?.fileUploadBlockedReason);
  const tray = useTray(remote, sessionId, machineId, {
    sessionId,
    maxImages: caps?.maxImages ?? 0,
    maxFiles: caps?.maxFiles ?? 0,
    supportsFiles: !!caps?.supportsFiles,
    originalImageMaxBytes: Number(caps?.originalImageMaxBytes ?? 0),
    galleryAcceptMime: caps?.galleryAcceptMime || undefined,
    originalImageHint: renderText(caps?.originalImageHint),
    fileMetadataNotice: renderText(caps?.fileMetadataNotice),
    blocked: attachWhy,
    fileBlocked: fileWhy,
  });
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
      const existingDraft = remote.drafts.get(sessionId) ?? "";
      if (eff.text) {
        remote.drafts.set(sessionId, existingDraft.trim() ? existingDraft.trimEnd() + "\n" + eff.text : eff.text);
      }
      const savedImages = saved?.items?.filter((it) => it.kind !== "file") ?? [];
      const imageCandidates = eff.imageIds.map((_, i) => {
        const src = savedImages[i]?.file ?? saved?.thumbs?.[i];
        return {
          name: src instanceof File ? src.name : "",
          mimeType: src?.type || "image/png",
          sizeBytes: src?.size ?? 0,
        };
      });
      const fileCandidates = files.map((f) => ({
        name: f.name,
        mimeType: f.file?.type ?? "",
        sizeBytes: Number(f.sizeBytes),
      }));
      if (imageCandidates.length || fileCandidates.length) {
        const check = tray.checkPutBack(imageCandidates, fileCandidates);
        if (!check.ok) {
          if (check.reason) toastRef.current(check.reason);
          setComposerKey((k) => k + 1);
          setTimeout(() => composerRef.current?.focus());
          return;
        }
      }
      if (eff.imageIds.length) {
        tray.restore(
          eff.imageIds.map((imageId, i) => ({
            imageId,
            thumb: saved?.thumbs?.[i],
            file: savedImages[i]?.file,
          })),
          !!saved?.original,
        );
      }
      if (files.length) {
        tray.restoreFiles(files.map((f) => ({ fileId: f.fileId, name: f.name, size: Number(f.sizeBytes), file: f.file })));
      }
      stash?.delete(eff.requestId);
      setComposerKey((k) => k + 1);
      setTimeout(() => composerRef.current?.focus());
    });
  }, [host, remote, sessionId, machineId, tray, stash]);

  useEffect(() => {
    return host.onResendAttachments(sessionId, async (eff: SessionResendAttachmentsEffect) => {
      const targetMachine = eff.machineId || machineId;
      const saved = stash?.get(eff.sourceRequestId);
      let imageIds = [...eff.imageIds];
      let fileIds = [...eff.fileIds];
      let nextSaved: LocalSendAttachments | undefined = saved
        ? {
            thumbs: saved.thumbs ? [...saved.thumbs] : undefined,
            imageSizes: saved.imageSizes ? [...saved.imageSizes] : undefined,
            files: saved.files ? [...saved.files] : undefined,
            items: saved.items ? [...saved.items] : undefined,
            original: saved.original,
          }
        : undefined;

      if (saved?.items?.some((it) => it.file || it.prepared)) {
        try {
          const uploadedImages: string[] = [];
          const uploadedFiles: NonNullable<LocalSendAttachments["files"]> = [];
          const nextThumbs: Blob[] = [];
          const nextSizes: { width: number; height: number }[] = [];
          for (const it of saved.items) {
            if (it.kind === "file") {
              if (!it.file) continue;
              const ab = await it.file.arrayBuffer();
              const up = await remote.uploadImage(
                targetMachine,
                {
                  bytes: new Uint8Array(ab),
                  format: "file",
                  fileName: it.name || "file",
                },
                () => undefined,
              );
              const fid = up.fileId || up.imageId;
              uploadedFiles.push({
                fileId: fid,
                name: it.name || "file",
                sizeBytes: BigInt(it.bytes ?? ab.byteLength),
                file: it.file instanceof File ? it.file : undefined,
              });
            } else {
              const prep = it.prepared ?? (it.file ? await remote.prepareImage(it.file, !!saved.original, tray.originalImageMaxBytes || Number(caps?.originalImageMaxBytes ?? 0)) : undefined);
              if (!prep) continue;
              const up = await remote.uploadImage(targetMachine, prep, () => undefined);
              uploadedImages.push(up.imageId);
              if (it.thumb) nextThumbs.push(it.thumb);
              nextSizes.push({ width: prep.width, height: prep.height });
            }
          }
          imageIds = uploadedImages;
          fileIds = uploadedFiles.map((f) => f.fileId);
          nextSaved = {
            thumbs: nextThumbs.length ? nextThumbs : saved.thumbs,
            imageSizes: nextSizes.length ? nextSizes : saved.imageSizes,
            files: uploadedFiles.length ? uploadedFiles : saved.files,
            items: saved.items.map((it) => ({ ...it })),
            original: saved.original,
          };
        } catch (err) {
          if (mountedSession.current) {
            toastRef.current(err instanceof Error ? err.message : String(err));
          }
          return;
        }
      }

      if (stash && nextSaved) {
        stash.set(eff.newRequestId, nextSaved);
      }
      host.sessionSubmitCommand(
        `send:${sessionId}:${eff.newRequestId}`,
        targetMachine,
        sessionId,
        create(CommandSchema, {
          kind: {
            case: "send",
            value: {
              sessionId,
              requestId: eff.newRequestId,
              text: eff.text,
              mode: eff.mode,
              imageIds,
              fileIds,
            },
          },
        }),
      );
      if (mountedSession.current) setSent((n) => n + 1);
    });
  }, [host, remote, sessionId, machineId, stash]);

  // Keep stash entries alive while retained by Core (including promote/cancel/5s undo), then prune once released.
  const seenRetainedRef = useRef(new Set<string>());
  useEffect(() => {
    if (!stash) return;
    const retained = new Set(vm.composer?.retainedAttachmentRequestIds ?? []);
    for (const id of retained) {
      seenRetainedRef.current.add(id);
    }
    for (const id of [...seenRetainedRef.current]) {
      if (!retained.has(id)) {
        stash.delete(id);
        stash.delete(`send:${sessionId}:${id}`);
        seenRetainedRef.current.delete(id);
      }
    }
  }, [stash, sessionId, vm.composer?.retainedAttachmentRequestIds]);

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

  const handleRestoreTrayItem = async (reqId: string) => {
    const it = (vm.composer?.pendingTray?.orderedItems ?? []).find((x) => x.requestId === reqId);
    const saved = stash?.get(reqId);
    const files: NonNullable<LocalSendAttachments["files"]> =
      saved?.files ?? (it?.fileIds ?? []).map((fileId) => ({ fileId, name: "", sizeBytes: 0n }));
    try {
      await remote.restoreDraft(sessionId, tray);
    } catch {
      return;
    }
    if (!mountedSession.current) return;
    const savedImages = saved?.items?.filter((x) => x.kind !== "file") ?? [];
    const imageCandidates = (it?.imageIds ?? []).map((_, i) => {
      const src = savedImages[i]?.file ?? saved?.thumbs?.[i];
      return {
        name: src instanceof File ? src.name : "",
        mimeType: src?.type || "image/png",
        sizeBytes: src?.size ?? 0,
      };
    });
    const fileCandidates = files.map((f) => ({
      name: f.name,
      mimeType: f.file?.type ?? "",
      sizeBytes: Number(f.sizeBytes),
    }));
    if (imageCandidates.length || fileCandidates.length) {
      const check = tray.checkPutBack(imageCandidates, fileCandidates);
      if (!check.ok) {
        if (check.reason) toastRef.current(check.reason);
        return;
      }
    }
    if (files.length) {
      pendingRestoreFiles.current.set(reqId, files);
    }
    host.sessionRestoreTrayItem(sessionId, reqId);
  };

  const handleBack = () => {
    if (go && header?.taskOwned && header.scheduledTaskId && header.machineId) {
      go(routeForTask(header.machineId, header.scheduledTaskId));
      return;
    }
    onBack();
  };

  const approvalId = vm.dock?.detail.case === "approval" ? vm.dock.detail.value.approvalId : "";
  const approvalReason = approvalId ? (remote.approvalReasons.get(approvalId) ?? "") : "";

  const timelineRef = useRef(vm.timeline);
  timelineRef.current = vm.timeline;
  const restoreTrayRef = useRef(handleRestoreTrayItem);
  restoreTrayRef.current = handleRestoreTrayItem;

  const onSubmitCommand = useCallback(
    (slotKey: string, cmd: Command) => host.sessionSubmitCommand(slotKey, machineId, sessionId, cmd),
    [host, machineId, sessionId],
  );
  const onAbandonCommand = useCallback(
    (slotKey: string) => host.sessionAbandonCommand(slotKey, sessionId),
    [host, sessionId],
  );
  const onCancelTrayItem = useCallback((reqId: string) => host.sessionCancelTrayItem(sessionId, reqId), [host, sessionId]);
  const onIgnoreTrayItem = useCallback((reqId: string) => host.sessionIgnoreTrayItem(sessionId, reqId), [host, sessionId]);
  const onPromoteTrayItem = useCallback((reqId: string) => host.sessionPromoteTrayItem(sessionId, reqId), [host, sessionId]);
  const onRestoreTrayItem = useCallback((reqId: string) => void restoreTrayRef.current(reqId), []);
  const onRetryTrayItem = useCallback((reqId: string) => host.sessionRetryTrayItem(sessionId, reqId), [host, sessionId]);
  const onResumeTrayItem = useCallback((reqId: string) => host.sessionResumeTrayItem(sessionId, reqId), [host, sessionId]);
  const onCancelAllPaused = useCallback(() => host.sessionCancelAllPaused(sessionId), [host, sessionId]);
  const onUndoCancel = useCallback(() => host.sessionUndoCancel(sessionId), [host, sessionId]);
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
          if (tray.note) toastRef.current(tray.note);
          return;
        }
      }
      const cur = remote.drafts.get(sessionId) ?? "";
      remote.drafts.set(sessionId, cur.trim() ? cur.trimEnd() + "\n" + text : text);
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
  const onRetrySession = useCallback(() => host.retrySession(sessionId), [host, sessionId]);
  const onRetryEvents = useCallback(() => host.retryEvents(sessionId), [host, sessionId]);
  const onRefreshModels = useCallback(() => host.sessionOpen(sessionId, true, false), [host, sessionId]);
  const onExpandGroup = useCallback(
    (groupId: string) => setExpandedGroups((old) => (old.includes(groupId) ? old : [...old, groupId])),
    [],
  );
  const onScheduleRevealed = useCallback(() => host.clearScheduleFocus(sessionId), [host, sessionId]);
  const onOpenImageViewer = useCallback(
    (anchor: ImageAnchor) => host.sessionImageViewerOpen(sessionId, anchor),
    [host, sessionId],
  );
  const onNavigateImageViewer = useCallback(
    (delta: number) => host.sessionImageViewerNavigate(sessionId, { delta }),
    [host, sessionId],
  );
  const onCloseImageViewer = useCallback(() => host.sessionImageViewerClose(sessionId), [host, sessionId]);
  const viewerOpenRef = useRef(false);
  viewerOpenRef.current = !!vm.imageViewer?.open;
  useEffect(() => {
    return () => {
      if (viewerOpenRef.current) host.sessionImageViewerClose(sessionId);
    };
  }, [host, sessionId]);

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
      onCancelTrayItem={onCancelTrayItem}
      onIgnoreTrayItem={onIgnoreTrayItem}
      onPromoteTrayItem={onPromoteTrayItem}
      onRestoreTrayItem={onRestoreTrayItem}
      onRetryTrayItem={onRetryTrayItem}
      onResumeTrayItem={onResumeTrayItem}
      onCancelAllPaused={onCancelAllPaused}
      onUndoCancel={onUndoCancel}
      onRestoreText={onRestoreText}
      onLoadEarlier={onLoadEarlier}
      onRetrySession={onRetrySession}
      onRetryEvents={onRetryEvents}
      onRefreshModels={onRefreshModels}
      onExpandGroup={onExpandGroup}
      onScheduleRevealed={onScheduleRevealed}
      onOpenImageViewer={onOpenImageViewer}
      onNavigateImageViewer={onNavigateImageViewer}
      onCloseImageViewer={onCloseImageViewer}
    />
  );
}

const ChatBody = memo(function ChatBody({
  vm,
  remote,
  sent,
  localAttachments,
  retainedReasons = [],
  onRestoreText,
  onTerminal,
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
  onRestoreText?: (text: string, requestId?: string) => void;
  onTerminal?: () => void;
  onLoadEarlier: () => void;
  onRetryEvents?: () => void;
  onExpandGroup: (groupId: string) => void;
  onScheduleRevealed?: () => void;
}) {
  const sid = vm.sessionId;
  const header = vm.header;
  const timeline = vm.timeline;
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
  // Receipts in a row sit 4 apart (design-system §7): the wrapper of one
  // followed by another says so, since each row has its own wrapper.
  const receiptRun = useMemo(
    () => new Set(rows.filter((r, i) => r.item.kind.case === "notifyReceipt" && rows[i + 1]?.item.kind.case === "notifyReceipt").map((r) => r.key)),
    [rows],
  );
  const rowClass = (it: TimelineRowEntry) => {
    const run = receiptRun.has(it.key) ? "nt-run" : "";
    if (!freshRow(it.key)) return run || undefined;
    const k = it.item.kind.case;
    if (k === "userMessage") return "mo-rise";
    if (k === "toolCall" || k === "toolGroup") return run ? "mo-fade " + run : "mo-fade";
    return run || undefined;
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
    </>
  );

  const online = header?.computerOnline ?? true;
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
          hasSharedFiles={hasSharedFiles}
          locale={locale}
          onTerminal={onTerminal}
          localAttachments={localAttachments}
          onRestoreText={onRestoreText}
          onExpandGroup={onExpandGroup}
        />
      </RowState>
    ),
    [sid, online, remote, remote?.lostAt, awaitingApprovalTool, hasSharedFiles, locale, onTerminal, localAttachments, onRestoreText, onExpandGroup],
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
          connected={header?.connState ? header.connState === ConnState.CONNECTED : remote.conn === "online"}
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

/** A scheduled run's push, left as one grey line (design-system §7): Core
 * says what it shows; the agent's longer texts open in place. */
function NotifyReceipt({ receipt: r }: { receipt: SessionNotifyReceiptItem }) {
  const [open, setOpen] = useRowExpanded("receipt");
  const label = renderText(r.label);
  const line = (
    <>
      <Bell />
      <span className="tx">
        {label} · {open || !r.preview ? renderText(r.time) : r.preview}
      </span>
    </>
  );
  if (!r.expandable) {
    return (
      <div className="nt">
        <div className="hd">{line}</div>
      </div>
    );
  }
  return (
    <div className={"nt can" + (open ? " open" : "")}>
      <button
        className="hd"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        aria-label={open ? i18n.t("markdown.collapse") : i18n.t("session.notify_receipt_expand")}
      >
        {line}
        <ChevronDown className="chev" />
      </button>
      {open && <div className="bd">{r.body}</div>}
    </div>
  );
}

const TimelineRow = memo(function TimelineRow({
  item,
  sessionId,
  online,
  lostAt,
  awaitingApproval,
  remote,
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
      const caption = renderText(m.originCaption);
      return (
        <div className="me" data-msg-id={item.id}>
          <ImageStrip refs={m.images} seq={item.seq} numbered />
          <FileChips files={m.files} />
          {m.isScheduled ? (
            <ScheduleText key={item.id} text={m.text} meta={caption} />
          ) : (
            m.textSegments.length > 0 && (
              <div className="mo-text">
                {m.textSegments.map((seg, i) =>
                  seg.kind.case === "text" ? (
                    <span key={i}>{seg.kind.value}</span>
                  ) : (
                    <span key={i} className="ichip">
                      {renderText(seg.imageLabel)}
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
          itemSeq={item.seq}
          online={online}
          lostAt={lostAt}
          awaitingApproval={awaitingApproval}
        />
      );
    case "toolGroup":
      return <ToolGroupView group={item.kind.value} itemSeq={item.seq} />;
    case "sharedFiles": {
      const sf = item.kind.value;
      if (hasSharedFiles) {
        return (
          <SharedFileCards
            remote={remote}
            sessionId={sessionId}
            files={sf.files}
          />
        );
      }
      return (
        <div className="file-chips">
          {sf.files.map((f, i) => (
            <FileChip
              key={f.fileId || String(i)}
              file={{
                name: f.displayName,
                displayName: f.displayName,
                nameHead: f.nameHead,
                nameTail: f.nameTail,
                sizeText: renderText(f.sizeText),
              }}
            />
          ))}
        </div>
      );
    }
    case "generatedImages":
      return <GenCard refs={item.kind.value.images} seq={item.seq} prompt={item.kind.value.revisedPrompt} />;
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
    case "notifyReceipt":
      return <NotifyReceipt receipt={item.kind.value} />;
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
        return null;
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
      return <div className="record muted">{renderText(item.kind.value.text) || i18n.t("session.skeleton")}</div>;
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
