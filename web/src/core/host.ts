import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { useCallback, useEffect, useMemo, useSyncExternalStore } from "react";
import type { Tray, TrayItem } from "../attach";
import { newRequestID } from "../crypto";
import {
  CacheBootstrapLoadedEventSchema,
  CacheEventsLoadedEventSchema,
  CommandOutcomeEventSchema,
  DirNavigateActionSchema,
  DirPickerCloseActionSchema,
  DirPickerOpenActionSchema,
  EffectBatchSchema,
  EntropyProvidedSchema,
  EventSchema,
  FetchSessionActionSchema,
  HomeLoadMoreEndedActionSchema,
  HomeRetryLoadActionSchema,
  HomeScreenRequestSchema,
  HomeSelectSessionActionSchema,
  HomeSetFilterActionSchema,
  HomeSetScopeActionSchema,
  LocalCalendarSchema,
  MachineRouteStatusEventSchema,
  MonthStartSchema,
  NavigateRoute,
  NetworkChangedEventSchema,
  NewSessionOpenActionSchema,
  NewSessionProjectActionSchema,
  NewSessionScreenRequestSchema,
  NewSessionSelectAgentActionSchema,
  NewSessionSelectMachineActionSchema,
  NewSessionSelectModelActionSchema,
  NewSessionSetPromptActionSchema,
  NewSessionSubmitActionSchema,
  OutcomeSource,
  ProjectAddActionSchema,
  ProjectKeyActionSchema,
  ProjectScreenRequestSchema,
  ProjectSetSearchActionSchema,
  RetrySessionEventsActionSchema,
  ScreenSchema,
  ScreenTarget,
  type Screen,
  type ViewModel,
  SecuritySnapshotEventSchema,
  SessionPinEventSchema,
  SessionScheduleHintSchema,
  SessionScheduleHintsEventSchema,
  SessionWatchEventSchema,
  StreamClosedEventSchema,
  StreamFrameEventSchema,
  StreamReadyEventSchema,
  SyncLifecycleActionSchema,
  SyncLifecycleKind,
  SyncStateScreenRequestSchema,
  TimerFiredEventSchema,
  TimerKind,
  TokenResultEventSchema,
  ViewModelSchema,
  type CacheBootstrapLoadedEvent,
  type CacheDeleteEffect,
  type CacheEventsLoadedEvent,
  type CacheWriteEffect,
  type CommandOutcomeEvent,
  type DispatchCommandEffect,
  type Effect,
  type Event,
  type LocalCalendar,
  type MonthStart,
  type RpcEffect,
  type SecuritySnapshotEvent,
  type StreamClosedEvent,
  type StreamFrameEvent,
  type TokenResultEvent,
} from "../gen/remoteagent/core/v1/app_pb";
import type { Agent, ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import { ConnState, type HomeViewModel } from "../gen/remoteagent/core/v1/home_view_pb";
import { CommandResultSchema, type Command } from "../gen/remoteagent/v1/remote_pb";
import type { NewSessionViewModel } from "../gen/remoteagent/core/v1/new_session_view_pb";
import type { ProjectViewModel } from "../gen/remoteagent/core/v1/project_view_pb";
import {
  ScheduleBoolActionSchema,
  ScheduleCacheLoadedEventSchema,
  ScheduleDetailScreenRequestSchema,
  ScheduleFormBoolActionSchema,
  ScheduleFormOpenActionSchema,
  ScheduleFormScreenRequestSchema,
  ScheduleFormSelectAgentActionSchema,
  ScheduleFormSelectModelActionSchema,
  ScheduleFormSelectProjectActionSchema,
  ScheduleFormStringActionSchema,
  ScheduleFormUpdateTimingActionSchema,
  ScheduleLocalContextEventSchema,
  ScheduleMarkReadActionSchema,
  ScheduleMutationRefSchema,
  ScheduleNavigateRoute,
  ScheduleOperateActionSchema,
  ScheduleSeenLoadedEventSchema,
  ScheduleSessionStripScreenRequestSchema,
  SchedulesScreenRequestSchema,
  ScheduleStringActionSchema,
  ScheduleTaskBoolActionSchema,
  ScheduleTaskRefSchema,
  ScheduleTaskStringActionSchema,
  type ScheduleCacheLoadedEvent,
  type ScheduleDetailViewModel,
  type ScheduleFormMode,
  type ScheduleFormViewModel,
  type ScheduleNotificationRef,
  type ScheduleOperationKind,
  type ScheduleSeenLoadedEvent,
  type ScheduleSeenToken,
  type ScheduleSessionStripViewModel,
  type SchedulesViewModel,
} from "../gen/remoteagent/core/v1/schedules_view_pb";
import {
  SessionOpenActionSchema,
  SessionOutgoingActionSchema,
  SessionQuestionStepActionSchema,
  SessionRefreshHistoryActionSchema,
  SessionScreenRequestSchema,
  SessionSlotActionSchema,
  SessionSubmitCommandActionSchema,
  SessionWindowRequestSchema,
  type SessionRestoreComposerEffect,
  type SessionViewModel,
} from "../gen/remoteagent/core/v1/session_view_pb";
import type { SyncStateView } from "../gen/remoteagent/core/v1/sync_view_pb";
import type { Remote } from "../remote";
import type { WasmAppCore } from "./projection";
import { getCore } from "./runtime";

const SCOPE_STORAGE_KEY = "ra-scope";
const CLOCK_TICK_MS = 60_000;
const CALENDAR_MONTH_HISTORY = 36;
const INITIAL_ENTROPY_COUNT = 32;

export type Go = (route: string, replace?: boolean) => void;

export interface StartTraySnapshot {
  tray: Tray;
  imageIds: string[];
  fileIds: string[];
  items: TrayItem[];
}

export interface StreamLegCallbacks {
  onReady(legId: bigint, serverTimeMs: bigint): void;
  onFrame(legId: bigint, frame: StreamFrameEvent["frame"], onProcessed?: () => void): void;
  onClosed(legId: bigint, info: Omit<StreamClosedEvent, "$typeName" | "legId">): void;
}

export interface HostBridge {
  start(): void;
  stop(): void;
  setForeground(active: boolean): void;
  setNetwork(available: boolean, signature?: string): void;
  reconnectNow(networkChanged?: boolean): void;
  retryLogin(): void;
  loadMoreSessions(): Promise<boolean>;
  loadAllSessions(): Promise<void>;
  retryList(): void;
  retryPrefs(): void;
  ensureSession(sessionId: string): Promise<void>;
  retrySession(sessionId: string): void;
  retryEvents(sessionId: string): void;
  pushRouteStatus(machineId: string, directReady: boolean): void;
  pushScheduleHints(hints: Array<{ sessionId: string; scheduleName: string; unread: boolean }>): void;
  pushSessionPin(sessionId: string, pinned: boolean): void;
  changeProjectPref(kind: "pin" | "unpin" | "hide" | "unhide", path: string): void;
  dispatchEvent(ev: Event, onProcessed?: () => void): void;
}

export interface CoreExecutor {
  openStreamLeg(legId: bigint, token: string, callbacks: StreamLegCallbacks): void;
  closeStreamLeg(legId: bigint): void;
  ensureToken(opId: bigint, forceLogin: boolean, timeoutMs: bigint): Promise<TokenResultEvent>;
  discardToken(token: string): void;
  syncSecurity(opId: bigint, networkGeneration: bigint, touch: boolean): Promise<SecuritySnapshotEvent>;
  readBootstrapCache(opId: bigint): Promise<CacheBootstrapLoadedEvent>;
  readEventsCache(
    opId: bigint,
    sessionId: string,
    newest: boolean,
    afterSeq: bigint,
    limit: number,
  ): Promise<CacheEventsLoadedEvent>;
  prepareCacheWrite?(eff: CacheWriteEffect): (() => Promise<void>) | undefined;
  releaseEventRefs?(ev: Event): void;
  writeCache(eff: CacheWriteEffect): Promise<void>;
  deleteCache(eff: CacheDeleteEffect): Promise<void>;
  executeRpc(eff: RpcEffect): Promise<Event>;
  executeCommandAttempt(eff: DispatchCommandEffect): Promise<CommandOutcomeEvent>;
  applySyncStateView(view: SyncStateView): void;
  readScheduleCache?(opId: bigint, machineId: string): Promise<ScheduleCacheLoadedEvent>;
  deleteScheduleCache?(machineId: string): Promise<void>;
  loadScheduleSeen?(machineId: string): Promise<ScheduleSeenLoadedEvent>;
  saveScheduleSeen?(machineId: string, tokens: ScheduleSeenToken[]): Promise<void>;
  dismissScheduleNotifications?(runs: ScheduleNotificationRef[]): void;
}

export interface SessionWindowOptions {
  limit?: number;
  beforeSeq?: bigint;
  aroundSeq?: bigint;
  revealSeq?: bigint;
  revealRunId?: string;
  expandedGroupIds?: string[];
}

export interface CoreHostOptions {
  /** Overrides the executor used to execute Core Effects (defaults to Remote). */
  executor?: CoreExecutor;
  /** Optional observer hook called for every executed Effect (primarily for tests). */
  onEffect?: (effect: Effect) => void;
  go?: Go;
  /** Optional explicit HMAC key for scope tokens (tests without IDB). */
  scopeHmacKey?: Uint8Array;
}

/** Current browser IANA timezone ID (falls back to UTC when unavailable). */
export function localTimeZoneId(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** Computes local timezone and tomorrow/day-after-tomorrow day boundaries for Core's ScheduleLocalContextEvent. */
export function scheduleLocalContext(nowMs = Date.now()) {
  const d = new Date(nowMs);
  const tomorrow = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1).getTime();
  const dayAfter = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 2).getTime();
  return create(ScheduleLocalContextEventSchema, {
    localTimezone: localTimeZoneId(),
    tomorrowStartMs: BigInt(tomorrow),
    dayAfterTomorrowStartMs: BigInt(dayAfter),
  });
}

/** Computes local-timezone calendar boundaries + 36 month starts for Core's ClockUpdated event. */
export function localCalendar(nowMs = Date.now()): LocalCalendar {
  const d = new Date(nowMs);
  const todayStart = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const yesterdayStart = new Date(d.getFullYear(), d.getMonth(), d.getDate() - 1).getTime();
  const weekStart = new Date(d.getFullYear(), d.getMonth(), d.getDate() - 6).getTime();
  const months: MonthStart[] = [];
  for (let i = CALENDAR_MONTH_HISTORY - 1; i >= 0; i--) {
    const md = new Date(d.getFullYear(), d.getMonth() - i, 1);
    months.push(
      create(MonthStartSchema, {
        year: md.getFullYear(),
        month: md.getMonth() + 1,
        startMs: BigInt(md.getTime()),
      }),
    );
  }
  return create(LocalCalendarSchema, {
    nowMs: BigInt(nowMs),
    todayStartMs: BigInt(todayStart),
    yesterdayStartMs: BigInt(yesterdayStart),
    weekStartMs: BigInt(weekStart),
    nowYear: d.getFullYear(),
    months,
  });
}

function readStoredScope(): string {
  try {
    const raw = localStorage.getItem(SCOPE_STORAGE_KEY) ?? "";
    return !raw || raw === "*" ? "all" : raw;
  } catch {
    return "all";
  }
}

function writeStoredScope(scopeToken: string) {
  try {
    localStorage.setItem(SCOPE_STORAGE_KEY, scopeToken || "*");
  } catch {
    /* Private storage may be unavailable. */
  }
}

function randomFloats(count: number): number[] {
  const arr = new Uint32Array(count);
  crypto.getRandomValues(arr);
  return Array.from(arr, (n) => n / 0x1_0000_0000);
}

function randomIds(count: number): string[] {
  return Array.from({ length: count }, () => newRequestID());
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function routeForSession(sessionId: string): string {
  return "s/" + encodeURIComponent(sessionId);
}

export function routeForProject(projectKey: string): string {
  return projectKey ? "p/" + encodeURIComponent(projectKey) : "projects";
}

export function routeForMachineSettings(machineId: string): string {
  return "m/" + machineId;
}

export function routeForMachineStatus(machineId: string): string {
  return "machine-status/" + encodeURIComponent(machineId);
}

export function routeForTask(machineId: string, taskId: string): string {
  return "tasks/" + encodeURIComponent(machineId) + "/" + encodeURIComponent(taskId);
}

const ALL_TARGETS = [
  ScreenTarget.HOME,
  ScreenTarget.PROJECT,
  ScreenTarget.SESSION,
  ScreenTarget.NEW_SESSION,
  ScreenTarget.SYNC_STATE,
  ScreenTarget.SCHEDULES,
  ScreenTarget.SCHEDULE_DETAIL,
  ScreenTarget.SCHEDULE_FORM,
  ScreenTarget.SCHEDULE_SESSION_STRIP,
];

function timerKey(kind: TimerKind, key: string): string {
  return `${kind}:${key}`;
}

/**
 * Single-threaded main-thread Core shell wrapping Go's WasmAppCore.
 * Feeds browser time/entropy/lifecycle/network/UI events into Core,
 * executes all emitted Effects, and publishes per-screen Render notifications.
 */
export class CoreHost {
  private readonly core: WasmAppCore;
  readonly remote?: Remote;
  private readonly executor?: CoreExecutor;
  private readonly onEffect?: (effect: Effect) => void;
  private readonly explicitScopeHmacKey?: Uint8Array;
  private go?: Go;

  private readonly screenVersions = new Map<ScreenTarget, number>(ALL_TARGETS.map((t) => [t, 0]));
  private readonly listeners = new Map<ScreenTarget, Set<() => void>>(ALL_TARGETS.map((t) => [t, new Set()]));
  private totalListeners = 0;

  private homeCache?: { version: number; endedLimit: number; vm: HomeViewModel };
  private projectCache?: { version: number; key: string; manage: boolean; endedLimit: number; vm: ProjectViewModel };
  private sessionCache = new Map<string, { version: number; windowKey: string; vm: SessionViewModel }>();
  private newSessionCache?: { version: number; key: string; vm: NewSessionViewModel };
  private syncStateCache?: { version: number; view: SyncStateView };
  private schedulesCache?: { version: number; endedLimit: number; vm: SchedulesViewModel };
  private scheduleDetailCache?: { version: number; key: string; vm: ScheduleDetailViewModel };
  private scheduleFormCache = new Map<string, { version: number; vm: ScheduleFormViewModel }>();
  private scheduleStripCache = new Map<string, { version: number; vm: ScheduleSessionStripViewModel }>();

  private started = false;
  private closed = false;
  private watchCounts = new Map<string, number>();
  private restoreComposerListeners = new Map<string, Set<(eff: SessionRestoreComposerEffect) => void>>();
  private scheduleFormCloseListeners = new Map<string, Set<() => void>>();
  private scheduleFormTimingListeners = new Map<string, Set<(formKey: string) => void>>();
  private scheduleFocus = new Map<string, { runId: string; seq: bigint }>();
  private openFormKeys = new Set<string>();
  private timers = new Map<string, { id: bigint; handle: ReturnType<typeof setTimeout> }>();
  private clockTimer?: ReturnType<typeof setInterval>;
  private cacheWriteQueue: Promise<void> = Promise.resolve();

  private pendingEvents: Array<{ ev: Event; onProcessed?: () => void }> = [];
  private draining = false;
  private dirtyTargets = new Set<ScreenTarget>();

  private readonly trays = new Map<string, () => StartTraySnapshot>();
  private pendingStart?: {
    projectKey: string;
    prompt: string;
    taken: TrayItem[];
  };
  private lastProjectPrefEvent?: Event;

  constructor(remote?: Remote, opts: CoreHostOptions = {}) {
    this.core = getCore().newAppCore();
    this.remote = remote;
    this.executor = opts.executor ?? remote;
    this.onEffect = opts.onEffect;
    this.go = opts.go;
    this.explicitScopeHmacKey = opts.scopeHmacKey;

    if (this.remote) {
      this.remote.attachHost({
        start: () => this.start(),
        stop: () => this.stop(),
        setForeground: (active) => this.setForeground(active),
        setNetwork: (available, signature) => this.setNetwork(available, signature),
        reconnectNow: () => this.reconnectNow(),
        retryLogin: () => this.retryLogin(),
        loadMoreSessions: () => this.loadMoreSessions(),
        loadAllSessions: () => this.loadAllSessions(),
        retryList: () => this.retryList(),
        retryPrefs: () => this.retryPrefs(),
        ensureSession: (sid) => this.ensureSession(sid),
        retrySession: (sid) => this.retrySession(sid),
        retryEvents: (sid) => this.retryEvents(sid),
        pushRouteStatus: (machineId, directReady) =>
          this.emit({ case: "machineRouteStatus", value: create(MachineRouteStatusEventSchema, { machineId, directReady }) }),
        pushScheduleHints: (hints) =>
          this.emit({ case: "sessionScheduleHints", value: create(SessionScheduleHintsEventSchema, { hints: hints.map((h) => create(SessionScheduleHintSchema, h)) }) }),
        pushSessionPin: (sessionId, pinned) =>
          this.emit({ case: "sessionPin", value: create(SessionPinEventSchema, { sessionId, pinned }) }),
        changeProjectPref: (kind, path) =>
          this.emit({ case: kind === "pin" || kind === "unpin" ? "projectTogglePin" : "projectToggleHide", value: create(ProjectKeyActionSchema, { projectKey: path }) }),
        dispatchEvent: (ev, onProcessed) => this.dispatch(ev, onProcessed),
      });
    }

    this.dispatch(
      create(EventSchema, {
        event: { case: "clockUpdated", value: localCalendar() },
        randomIds: randomIds(INITIAL_ENTROPY_COUNT),
        unitFloats: randomFloats(INITIAL_ENTROPY_COUNT),
      }),
    );
    this.emit({ case: "scheduleLocalContext", value: scheduleLocalContext() });
  }

  private emit(event: Event["event"], onProcessed?: () => void) {
    this.dispatch(create(EventSchema, { event }), onProcessed);
  }

  private emitClock() {
    const now = Date.now();
    this.emit({ case: "clockUpdated", value: localCalendar(now) });
    this.emit({ case: "scheduleLocalContext", value: scheduleLocalContext(now) });
  }

  setNavigator(go?: Go) {
    this.go = go;
  }

  registerTray(projectKey: string, getter: () => StartTraySnapshot): () => void {
    this.trays.set(projectKey, getter);
    return () => {
      if (this.trays.get(projectKey) === getter) this.trays.delete(projectKey);
    };
  }

  // --- Lifecycle & Sync triggers ---

  start() {
    if (this.closed || this.started) return;
    this.started = true;
    clearInterval(this.clockTimer);
    this.clockTimer = setInterval(() => {
      if (!this.closed && this.started) this.emitClock();
    }, CLOCK_TICK_MS);
    const available = this.remote ? this.remote.network : typeof navigator === "undefined" || navigator.onLine !== false;
    this.emitClock();
    this.emit({
      case: "syncLifecycle",
      value: create(SyncLifecycleActionSchema, {
        kind: SyncLifecycleKind.START,
        homeScopeToken: readStoredScope(),
        scopeHmacKey: this.explicitScopeHmacKey ?? new Uint8Array(),
      }),
    });
    this.emit({
      case: "networkChanged",
      value: create(NetworkChangedEventSchema, { available, signature: String(available) }),
    });
    try {
      if (localStorage.getItem("schedule-ended-open") === "true") {
        this.scheduleToggleEnded(true);
      }
    } catch {}
  }

  stop() {
    if (!this.started && this.timers.size === 0 && this.clockTimer === undefined) return;
    this.started = false;
    this.clearTimers();
    clearInterval(this.clockTimer);
    this.clockTimer = undefined;
    if (!this.closed) this.emit({ case: "syncLifecycle", value: create(SyncLifecycleActionSchema, { kind: SyncLifecycleKind.STOP }) });
  }

  setForeground(active: boolean) {
    if (this.closed) return;
    if (active) this.emitClock();
    this.emit({
      case: "syncLifecycle",
      value: create(SyncLifecycleActionSchema, { kind: active ? SyncLifecycleKind.FOREGROUND : SyncLifecycleKind.BACKGROUND }),
    });
  }

  setNetwork(available: boolean, signature?: string) {
    if (this.closed) return;
    this.emit({ case: "networkChanged", value: create(NetworkChangedEventSchema, { available, signature: signature ?? String(available) }) });
  }

  reconnectNow() {
    if (!this.closed) this.emit({ case: "syncLifecycle", value: create(SyncLifecycleActionSchema, { kind: SyncLifecycleKind.RECONNECT_NOW }) });
  }

  retryLogin() {
    if (!this.closed) this.emit({ case: "syncLifecycle", value: create(SyncLifecycleActionSchema, { kind: SyncLifecycleKind.RETRY_LOGIN }) });
  }

  loadMoreSessions(): Promise<boolean> {
    this.emit({ case: "homeLoadMoreEnded", value: create(HomeLoadMoreEndedActionSchema, { step: 50 }) });
    return Promise.resolve(true);
  }

  async loadAllSessions(): Promise<void> {
    this.emit({ case: "homeLoadMoreEnded", value: create(HomeLoadMoreEndedActionSchema, { step: 100 }) });
  }

  retryList() {
    this.emit({ case: "homeRetryLoad", value: create(HomeRetryLoadActionSchema) });
  }

  retryPrefs() {
    if (this.lastProjectPrefEvent && this.viewProject("", true).manage?.saveError) {
      this.dispatch(this.lastProjectPrefEvent);
      return;
    }
    this.emit({ case: "homeRetryLoad", value: create(HomeRetryLoadActionSchema) });
  }

  ensureSession(sessionId: string): Promise<void> {
    if (sessionId) this.emit({ case: "fetchSession", value: create(FetchSessionActionSchema, { sessionId }) });
    return Promise.resolve();
  }

  retrySession(sessionId: string) {
    if (!sessionId) return;
    this.ensureSession(sessionId);
    this.retryEvents(sessionId);
  }

  retryEvents(sessionId: string) {
    if (sessionId) this.emit({ case: "retrySessionEvents", value: create(RetrySessionEventsActionSchema, { sessionId }) });
  }

  // --- Screen Intents ---

  homeSetScope = (scopeKey: string) => this.emit({ case: "homeSetScope", value: create(HomeSetScopeActionSchema, { scopeKey }) });
  homeSetFilter = (query: string) => this.emit({ case: "homeSetFilter", value: create(HomeSetFilterActionSchema, { query }) });
  homeLoadMoreEnded = (step = 30) => this.emit({ case: "homeLoadMoreEnded", value: create(HomeLoadMoreEndedActionSchema, { step }) });
  homeRetryLoad = () => this.emit({ case: "homeRetryLoad", value: create(HomeRetryLoadActionSchema) });
  homeSelectSession = (sessionId: string) => this.emit({ case: "homeSelectSession", value: create(HomeSelectSessionActionSchema, { sessionId }) });

  projectSetSearch = (query: string) => this.emit({ case: "projectSetSearch", value: create(ProjectSetSearchActionSchema, { query }) });
  projectTogglePin = (projectKey: string) => this.emit({ case: "projectTogglePin", value: create(ProjectKeyActionSchema, { projectKey }) });
  projectToggleHide = (projectKey: string) => this.emit({ case: "projectToggleHide", value: create(ProjectKeyActionSchema, { projectKey }) });
  projectOpenDirPicker = (forNewTask: boolean, machineId = "", path = "") =>
    this.emit({ case: "projectOpenDirPicker", value: create(DirPickerOpenActionSchema, { forNewTask, machineId, path }) });
  projectDirNavigate = (machineId: string, path: string) =>
    this.emit({ case: "projectDirNavigate", value: create(DirNavigateActionSchema, { machineId, path }) });
  projectCloseDirPicker = () => this.emit({ case: "projectCloseDirPicker", value: create(DirPickerCloseActionSchema) });
  projectAdd = (path: string) => this.emit({ case: "projectAdd", value: create(ProjectAddActionSchema, { path }) });

  newSessionOpen = (projectKey: string) => this.emit({ case: "newSessionOpen", value: create(NewSessionOpenActionSchema, { projectKey }) });
  newSessionSelectMachine = (machineId: string) => this.emit({ case: "newSessionSelectMachine", value: create(NewSessionSelectMachineActionSchema, { machineId }) });
  newSessionSelectAgent = (agent: Agent) => this.emit({ case: "newSessionSelectAgent", value: create(NewSessionSelectAgentActionSchema, { agent }) });
  newSessionSelectModel = (model: string, effort: string, standardContext?: boolean) =>
    this.emit({ case: "newSessionSelectModel", value: create(NewSessionSelectModelActionSchema, { model, effort, ...(standardContext !== undefined ? { standardContext } : {}) }) });
  newSessionRetryModels = (projectKey: string) => this.emit({ case: "newSessionRetryModels", value: create(NewSessionProjectActionSchema, { projectKey }) });
  newSessionRetrySaveConfig = (projectKey: string) => this.emit({ case: "newSessionRetrySaveConfig", value: create(NewSessionProjectActionSchema, { projectKey }) });
  newSessionSetPrompt = (prompt: string, attachmentCount = 0, attachmentsMoved = false) =>
    this.emit({ case: "newSessionSetPrompt", value: create(NewSessionSetPromptActionSchema, { prompt, attachmentCount, attachmentsMoved }) });
  newSessionSubmit = (projectKey: string) => this.emit({ case: "newSessionSubmit", value: create(NewSessionSubmitActionSchema, { projectKey }) });
  newSessionAbandonStart = (projectKey: string) => this.emit({ case: "newSessionAbandonStart", value: create(NewSessionProjectActionSchema, { projectKey }) });

  watchSession(sessionId: string) {
    if (!sessionId) return;
    const prev = this.watchCounts.get(sessionId) ?? 0;
    this.watchCounts.set(sessionId, prev + 1);
    if (prev === 0) {
      this.sessionCache.delete(sessionId);
      this.emit({ case: "sessionWatch", value: create(SessionWatchEventSchema, { sessionId, watched: true }) });
    }
  }

  unwatchSession(sessionId: string) {
    if (!sessionId) return;
    const prev = this.watchCounts.get(sessionId) ?? 0;
    if (prev <= 1) {
      this.watchCounts.delete(sessionId);
      this.sessionCache.delete(sessionId);
      if (prev === 1) {
        this.emit({ case: "sessionWatch", value: create(SessionWatchEventSchema, { sessionId, watched: false }) });
      }
      return;
    }
    this.watchCounts.set(sessionId, prev - 1);
  }

  sessionOpen(sessionId: string, refreshModels = false, refreshSettings = false) {
    if (!sessionId) return;
    this.emit({
      case: "sessionOpen",
      value: create(SessionOpenActionSchema, { sessionId, refreshModels, refreshSettings }),
    });
  }

  sessionSubmitCommand(slotKey: string, machineId: string, sessionId: string, command: Command) {
    this.emit({
      case: "sessionSubmitCommand",
      value: create(SessionSubmitCommandActionSchema, { slotKey, machineId, sessionId, command }),
    });
  }

  sessionAbandonCommand(slotKey: string, sessionId: string) {
    this.emit({
      case: "sessionAbandonCommand",
      value: create(SessionSlotActionSchema, { slotKey, sessionId }),
    });
  }

  sessionDismissOutgoing(sessionId: string, requestId: string) {
    this.emit({
      case: "sessionDismissOutgoing",
      value: create(SessionOutgoingActionSchema, { sessionId, requestId }),
    });
  }

  sessionRestoreOutgoing(sessionId: string, requestId: string) {
    this.emit({
      case: "sessionRestoreOutgoing",
      value: create(SessionOutgoingActionSchema, { sessionId, requestId }),
    });
  }

  sessionSetQuestionIndex(sessionId: string, questionId: string, questionIndex: number) {
    this.emit({
      case: "sessionSetQuestionIndex",
      value: create(SessionQuestionStepActionSchema, { sessionId, questionId, questionIndex }),
    });
  }

  sessionLoadEarlier(sessionId: string) {
    if (!sessionId) return;
    this.emit({
      case: "sessionLoadEarlier",
      value: create(SessionRefreshHistoryActionSchema, { sessionId }),
    });
  }

  onRestoreComposer(sessionId: string, fn: (eff: SessionRestoreComposerEffect) => void): () => void {
    let set = this.restoreComposerListeners.get(sessionId);
    if (!set) {
      set = new Set();
      this.restoreComposerListeners.set(sessionId, set);
    }
    set.add(fn);
    return () => {
      set?.delete(fn);
      if (set?.size === 0) this.restoreComposerListeners.delete(sessionId);
    };
  }

  // --- Schedule Intents & Listeners ---

  scheduleRefresh = (machineId = "") =>
    this.emit({ case: "scheduleRefresh", value: create(ScheduleStringActionSchema, { value: machineId }) });
  scheduleSetVisibleSession = (sessionId: string) =>
    this.emit({ case: "scheduleSetVisibleSession", value: create(ScheduleStringActionSchema, { value: sessionId }) });
  scheduleListOpen = (open: boolean) =>
    this.emit({ case: "scheduleListOpen", value: create(ScheduleBoolActionSchema, { value: open }) });
  scheduleSelectTask = (machineId: string, taskId: string) =>
    this.emit({ case: "scheduleSelectTask", value: create(ScheduleTaskRefSchema, { machineId, taskId }) });
  scheduleToggleEnded = (open?: boolean) => {
    this.emit({ case: "scheduleToggleEnded", value: create(ScheduleBoolActionSchema, open === undefined ? {} : { value: open }) });
    try {
      localStorage.setItem("schedule-ended-open", String(this.viewSchedules().endedExpanded));
    } catch {}
  };
  scheduleMarkRead = (tasks: Array<{ machineId: string; taskId: string }>, allFresh = false) =>
    this.emit({
      case: "scheduleMarkRead",
      value: create(ScheduleMarkReadActionSchema, {
        tasks: tasks.map((t) => create(ScheduleTaskRefSchema, t)),
        allFresh,
      }),
    });
  scheduleMarkUnread = (machineId: string, taskId: string, unread = true) =>
    this.emit({ case: "scheduleMarkUnread", value: create(ScheduleTaskBoolActionSchema, { machineId, taskId, value: unread }) });
  scheduleDetailOpen = (machineId: string, taskId: string) =>
    this.emit({ case: "scheduleDetailOpen", value: create(ScheduleTaskRefSchema, { machineId, taskId }) });
  scheduleDetailHide = (machineId: string, taskId: string) =>
    this.emit({ case: "scheduleDetailHide", value: create(ScheduleTaskRefSchema, { machineId, taskId }) });
  scheduleDetailClose = (machineId: string, taskId: string) =>
    this.emit({ case: "scheduleDetailClose", value: create(ScheduleTaskRefSchema, { machineId, taskId }) });
  scheduleDetailShowLatestResult = (machineId: string, taskId: string) =>
    this.emit({ case: "scheduleDetailShowLatestResult", value: create(ScheduleTaskRefSchema, { machineId, taskId }) });
  scheduleDetailToggleQuietGroup = (machineId: string, taskId: string, groupKey: string) =>
    this.emit({ case: "scheduleDetailToggleQuietGroup", value: create(ScheduleTaskStringActionSchema, { machineId, taskId, value: groupKey }) });
  scheduleDetailShowAllRuns = (machineId: string, taskId: string, open: boolean) =>
    this.emit({ case: "scheduleDetailShowAllRuns", value: create(ScheduleTaskBoolActionSchema, { machineId, taskId, value: open }) });
  scheduleDetailOpenRun = (machineId: string, taskId: string, runId: string) =>
    this.emit({ case: "scheduleDetailOpenRun", value: create(ScheduleTaskStringActionSchema, { machineId, taskId, value: runId }) });
  scheduleOperate = (machineId: string, taskId: string, operation: ScheduleOperationKind) =>
    this.emit({ case: "scheduleOperate", value: create(ScheduleOperateActionSchema, { machineId, taskId, operation }) });
  scheduleSetNotify = (machineId: string, taskId: string, notify: boolean) =>
    this.emit({ case: "scheduleSetNotify", value: create(ScheduleTaskBoolActionSchema, { machineId, taskId, value: notify }) });
  scheduleAbandonMutation = (machineId = "", taskId = "", formKey = "") =>
    this.emit({ case: "scheduleAbandonMutation", value: create(ScheduleMutationRefSchema, { formKey, machineId, taskId }) });
  scheduleRetryMutation = (machineId = "", taskId = "", formKey = "") =>
    this.emit({ case: "scheduleRetryMutation", value: create(ScheduleMutationRefSchema, { formKey, machineId, taskId }) });

  scheduleFormOpen(
    mode: ScheduleFormMode,
    opts: { machineId?: string; taskId?: string; sessionId?: string; projectKey?: string } = {},
  ): string {
    this.emit({
      case: "scheduleFormOpen",
      value: create(ScheduleFormOpenActionSchema, {
        mode,
        machineId: opts.machineId ?? "",
        taskId: opts.taskId ?? "",
        sessionId: opts.sessionId ?? "",
        projectKey: opts.projectKey ?? "",
        localTimezone: localTimeZoneId(),
      }),
    });
    const vm = this.viewScheduleForm("");
    if (vm.open && vm.formKey) this.openFormKeys.add(vm.formKey);
    return vm.formKey;
  }

  scheduleFormClose = (formKey: string, discard = false) => {
    if (discard) this.openFormKeys.delete(formKey);
    this.emit({ case: "scheduleFormClose", value: create(ScheduleFormBoolActionSchema, { formKey, value: discard }) });
  };

  scheduleFormSetNotify = (formKey: string, value: boolean) =>
    this.emit({ case: "scheduleFormSetNotify", value: create(ScheduleFormBoolActionSchema, { formKey, value }) });
  scheduleFormSubmit = (formKey: string, allowDuplicate = false) =>
    this.emit({ case: "scheduleFormSubmit", value: create(ScheduleFormBoolActionSchema, { formKey, value: allowDuplicate }) });
  scheduleFormSetMessage = (formKey: string, value: string) =>
    this.emit({ case: "scheduleFormSetMessage", value: create(ScheduleFormStringActionSchema, { formKey, value }) });
  scheduleFormSetTitle = (formKey: string, value: string) =>
    this.emit({ case: "scheduleFormSetTitle", value: create(ScheduleFormStringActionSchema, { formKey, value }) });
  scheduleFormSelectMachine = (formKey: string, machineId: string) =>
    this.emit({ case: "scheduleFormSelectMachine", value: create(ScheduleFormStringActionSchema, { formKey, value: machineId }) });
  scheduleFormRetryModels = (formKey: string) =>
    this.emit({ case: "scheduleFormRetryModels", value: create(ScheduleFormStringActionSchema, { formKey, value: "" }) });
  scheduleFormSelectProject = (formKey: string, projectKey: string, machineId = "") =>
    this.emit({ case: "scheduleFormSelectProject", value: create(ScheduleFormSelectProjectActionSchema, { formKey, projectKey, machineId }) });
  scheduleFormSelectAgent = (formKey: string, agent: Agent) =>
    this.emit({ case: "scheduleFormSelectAgent", value: create(ScheduleFormSelectAgentActionSchema, { formKey, agent }) });
  scheduleFormSelectModel = (formKey: string, model: string, effort: string, mode?: ApprovalMode) =>
    this.emit({
      case: "scheduleFormSelectModel",
      value: create(ScheduleFormSelectModelActionSchema, { formKey, model, effort, ...(mode !== undefined ? { mode } : {}) }),
    });
  scheduleFormUpdateTiming = (opts: Omit<Parameters<typeof create<typeof ScheduleFormUpdateTimingActionSchema>>[1], "$typeName">) =>
    this.emit({ case: "scheduleFormUpdateTiming", value: create(ScheduleFormUpdateTimingActionSchema, opts) });

  private addFormListener<T extends unknown[]>(map: Map<string, Set<(...args: T) => void>>, formKey: string, fn: (...args: T) => void): () => void {
    let set = map.get(formKey);
    if (!set) {
      set = new Set();
      map.set(formKey, set);
    }
    set.add(fn);
    return () => {
      set?.delete(fn);
      if (set?.size === 0) map.delete(formKey);
    };
  }

  onScheduleFormClose(formKey: string, fn: () => void): () => void {
    return this.addFormListener(this.scheduleFormCloseListeners, formKey, fn);
  }

  onScheduleFormOpenTiming(formKey: string, fn: (formKey: string) => void): () => void {
    return this.addFormListener(this.scheduleFormTimingListeners, formKey, fn);
  }

  getScheduleFocus(sessionId: string): { runId: string; seq: bigint } | undefined {
    return this.scheduleFocus.get(sessionId);
  }

  clearScheduleFocus(sessionId: string) {
    this.scheduleFocus.delete(sessionId);
  }

  hasUnsavedScheduleDrafts(): boolean {
    if (this.viewScheduleForm("").hasUnsavedChanges) return true;
    for (const key of this.openFormKeys) {
      const vm = this.viewScheduleForm(key);
      if (!vm.open) this.openFormKeys.delete(key);
      else if (vm.hasUnsavedChanges) return true;
    }
    return false;
  }

  // --- Subscriptions & ViewModel lookups ---

  subscribe(target: ScreenTarget, fn: () => void): () => void {
    const set = this.listeners.get(target);
    if (!set) return () => {};
    set.add(fn);
    this.totalListeners++;
    return () => {
      if (set.delete(fn)) this.totalListeners--;
    };
  }

  version(target: ScreenTarget): number {
    return this.screenVersions.get(target) ?? 0;
  }

  viewHome(endedLimit = 30): HomeViewModel {
    const ver = this.version(ScreenTarget.HOME);
    if (this.homeCache && this.homeCache.version === ver && this.homeCache.endedLimit === endedLimit) {
      return this.homeCache.vm;
    }
    const value = this.queryView<HomeViewModel>({ case: "home", value: create(HomeScreenRequestSchema, { endedLimit }) }, "home");
    this.homeCache = { version: ver, endedLimit, vm: value };
    return value;
  }

  viewProject(projectKey: string, manage: boolean, endedLimit = 30): ProjectViewModel {
    const ver = this.version(ScreenTarget.PROJECT);
    if (
      this.projectCache &&
      this.projectCache.version === ver &&
      this.projectCache.key === projectKey &&
      this.projectCache.manage === manage &&
      this.projectCache.endedLimit === endedLimit
    ) {
      return this.projectCache.vm;
    }
    const value = this.queryView<ProjectViewModel>(
      { case: "project", value: create(ProjectScreenRequestSchema, { projectKey, manage, endedLimit }) },
      "project",
    );
    this.projectCache = { version: ver, key: projectKey, manage, endedLimit, vm: value };
    return value;
  }

  viewNewSession(projectKey = ""): NewSessionViewModel {
    const ver = this.version(ScreenTarget.NEW_SESSION);
    if (this.newSessionCache && this.newSessionCache.version === ver && this.newSessionCache.key === projectKey) {
      return this.newSessionCache.vm;
    }
    const value = this.queryView<NewSessionViewModel>({ case: "newSession", value: create(NewSessionScreenRequestSchema, { projectKey }) }, "newSession");
    this.newSessionCache = { version: ver, key: projectKey, vm: value };
    return value;
  }

  viewSyncState(): SyncStateView {
    const ver = this.version(ScreenTarget.SYNC_STATE);
    if (this.syncStateCache && this.syncStateCache.version === ver) {
      return this.syncStateCache.view;
    }
    const value = this.queryView<SyncStateView>({ case: "syncState", value: create(SyncStateScreenRequestSchema, {}) }, "syncState");
    this.syncStateCache = { version: ver, view: value };
    return value;
  }

  viewSession(sessionId: string, window?: SessionWindowOptions): SessionViewModel {
    const ver = this.version(ScreenTarget.SESSION);
    const expandedGroupIds = window?.expandedGroupIds ?? [];
    const windowKey = window
      ? `${window.limit ?? 0}:${window.beforeSeq ?? 0n}:${window.aroundSeq ?? 0n}:${window.revealSeq ?? 0n}:${window.revealRunId ?? ""}:${expandedGroupIds.join(",")}`
      : "";
    const cached = this.sessionCache.get(sessionId);
    if (cached && cached.version === ver && cached.windowKey === windowKey) {
      return cached.vm;
    }
    const value = this.queryView<SessionViewModel>(
      {
        case: "session",
        value: create(SessionScreenRequestSchema, {
          sessionId,
          window: window
            ? create(SessionWindowRequestSchema, {
                limit: window.limit ?? 0,
                beforeSeq: window.beforeSeq ?? 0n,
                aroundSeq: window.aroundSeq ?? 0n,
                revealSeq: window.revealSeq ?? 0n,
                revealRunId: window.revealRunId ?? "",
                expandedGroupIds,
              })
            : undefined,
        }),
      },
      "session",
    );
    this.sessionCache.set(sessionId, { version: ver, windowKey, vm: value });
    return value;
  }

  private queryView<T>(screen: Screen["screen"], expectedCase: NonNullable<ViewModel["view"]["case"]>): T {
    const req = create(ScreenSchema, { screen } as Parameters<typeof create<typeof ScreenSchema>>[1]);
    const vm = fromBinary(ViewModelSchema, this.core.view(toBinary(ScreenSchema, req)));
    if (vm.view.case !== expectedCase) throw new Error("unexpected view model case: " + vm.view.case);
    return vm.view.value as T;
  }

  viewSchedules(endedLimit = 30): SchedulesViewModel {
    const ver = this.version(ScreenTarget.SCHEDULES);
    if (this.schedulesCache && this.schedulesCache.version === ver && this.schedulesCache.endedLimit === endedLimit) {
      return this.schedulesCache.vm;
    }
    const value = this.queryView<SchedulesViewModel>({ case: "schedules", value: create(SchedulesScreenRequestSchema, { endedLimit }) }, "schedules");
    this.schedulesCache = { version: ver, endedLimit, vm: value };
    return value;
  }

  viewScheduleDetail(machineId: string, taskId: string, earlierRunsLimit = 10): ScheduleDetailViewModel {
    const ver = this.version(ScreenTarget.SCHEDULE_DETAIL);
    const key = `${machineId}\0${taskId}\0${earlierRunsLimit}`;
    if (this.scheduleDetailCache && this.scheduleDetailCache.version === ver && this.scheduleDetailCache.key === key) {
      return this.scheduleDetailCache.vm;
    }
    const value = this.queryView<ScheduleDetailViewModel>(
      { case: "scheduleDetail", value: create(ScheduleDetailScreenRequestSchema, { machineId, taskId, earlierRunsLimit }) },
      "scheduleDetail",
    );
    this.scheduleDetailCache = { version: ver, key, vm: value };
    return value;
  }

  viewScheduleForm(formKey = ""): ScheduleFormViewModel {
    const ver = this.version(ScreenTarget.SCHEDULE_FORM);
    const cached = this.scheduleFormCache.get(formKey);
    if (cached && cached.version === ver) return cached.vm;
    const value = this.queryView<ScheduleFormViewModel>(
      { case: "scheduleForm", value: create(ScheduleFormScreenRequestSchema, { formKey }) },
      "scheduleForm",
    );
    if (value.open && value.formKey) this.openFormKeys.add(value.formKey);
    this.scheduleFormCache.set(formKey, { version: ver, vm: value });
    return value;
  }

  viewScheduleSessionStrip(sessionId: string): ScheduleSessionStripViewModel {
    const ver = this.version(ScreenTarget.SCHEDULE_SESSION_STRIP);
    const cached = this.scheduleStripCache.get(sessionId);
    if (cached && cached.version === ver) return cached.vm;
    const value = this.queryView<ScheduleSessionStripViewModel>(
      { case: "scheduleSessionStrip", value: create(ScheduleSessionStripScreenRequestSchema, { sessionId }) },
      "scheduleSessionStrip",
    );
    this.scheduleStripCache.set(sessionId, { version: ver, vm: value });
    return value;
  }

  // --- Dispatch & Effect Execution ---

  dispatch(ev: Event, onProcessed?: () => void) {
    if (this.closed) {
      onProcessed?.();
      this.executor?.releaseEventRefs?.(ev);
      return;
    }
    if (ev.nowMs === 0n) ev.nowMs = BigInt(Date.now());
    if (ev.monoMs === 0n) ev.monoMs = BigInt(Math.max(0, Math.floor(performance.now())));

    if (ev.event.case === "newSessionSubmit") {
      ev = this.enrichNewSessionSubmit(ev);
    }
    if (
      ev.event.case === "projectTogglePin" ||
      ev.event.case === "projectToggleHide" ||
      ev.event.case === "projectAdd"
    ) {
      this.lastProjectPrefEvent = ev;
    }

    this.pendingEvents.push({ ev, onProcessed });
    if (this.draining) return;

    this.draining = true;
    try {
      while (this.pendingEvents.length > 0 && !this.closed) {
        const next = this.pendingEvents.shift()!;
        try {
          const batchBytes = this.core.dispatch(toBinary(EventSchema, next.ev));
          const batch = fromBinary(EffectBatchSchema, batchBytes);
          for (const eff of batch.effects) {
            if (this.closed) break;
            this.handleEffect(eff);
          }
        } finally {
          next.onProcessed?.();
          this.executor?.releaseEventRefs?.(next.ev);
        }
      }
      this.flushRenders();
    } finally {
      this.draining = false;
    }
  }

  private enrichNewSessionSubmit(ev: Event): Event {
    if (ev.event.case !== "newSessionSubmit") return ev;
    const sub = ev.event.value;
    const projectKey = sub.projectKey;
    const vm = this.viewNewSession(projectKey);
    const autoSupported = sub.autoModeSupported || !!vm.modelPanel?.canSelectAutoMode;
    const snap = this.trays.get(projectKey)?.();
    this.pendingStart = {
      projectKey,
      prompt: vm.prompt.trim(),
      taken: snap?.items ?? [],
    };
    return create(EventSchema, {
      nowMs: ev.nowMs,
      monoMs: ev.monoMs,
      randomIds: ev.randomIds,
      unitFloats: ev.unitFloats,
      event: {
        case: "newSessionSubmit",
        value: create(NewSessionSubmitActionSchema, {
          projectKey,
          autoModeSupported: autoSupported,
          imageIds: sub.imageIds.length ? sub.imageIds : (snap?.imageIds ?? []),
          fileIds: sub.fileIds.length ? sub.fileIds : (snap?.fileIds ?? []),
        }),
      },
    });
  }

  private handleEffect(eff: Effect) {
    const val = eff.effect;
    switch (val.case) {
      case "render":
        for (const target of val.value.screens) {
          if (target !== ScreenTarget.UNSPECIFIED) {
            this.invalidateTarget(target);
            this.dirtyTargets.add(target);
          }
        }
        break;

      case "scheduleTimer": {
        const { timerId, kind, key, delayMs } = val.value;
        const tk = timerKey(kind, key);
        const prev = this.timers.get(tk);
        if (prev) clearTimeout(prev.handle);
        const handle = setTimeout(() => {
          const cur = this.timers.get(tk);
          if (!cur || cur.id !== timerId) return;
          this.timers.delete(tk);
          if (!this.closed) this.emit({ case: "timerFired", value: create(TimerFiredEventSchema, { timerId, kind, key }) });
        }, Math.max(0, Number(delayMs)));
        this.timers.set(tk, { id: timerId, handle });
        break;
      }

      case "cancelTimer": {
        const { timerId, kind, key } = val.value;
        const tk = timerKey(kind, key);
        const cur = this.timers.get(tk);
        if (cur && (timerId === 0n || cur.id === timerId)) {
          clearTimeout(cur.handle);
          this.timers.delete(tk);
        }
        break;
      }

      case "openStream":
        this.executor?.openStreamLeg(val.value.legId, val.value.token, {
          onReady: (id, serverTimeMs) => !this.closed && this.emit({ case: "streamReady", value: create(StreamReadyEventSchema, { legId: id, serverTimeMs }) }),
          onFrame: (id, frame, onProcessed) =>
            this.closed ? onProcessed?.() : this.emit({ case: "streamFrame", value: create(StreamFrameEventSchema, { legId: id, frame }) }, onProcessed),
          onClosed: (id, info) => !this.closed && this.emit({ case: "streamClosed", value: create(StreamClosedEventSchema, { legId: id, ...info }) }),
        });
        break;

      case "closeStream":
        this.executor?.closeStreamLeg(val.value.legId);
        break;

      case "ensureToken": {
        const { opId, forceLogin, timeoutMs } = val.value;
        void this.executor?.ensureToken(opId, forceLogin, timeoutMs).then(
          (value) => !this.closed && this.emit({ case: "tokenResult", value }),
          (err) => !this.closed && this.emit({ case: "tokenResult", value: create(TokenResultEventSchema, { opId, error: errorMessage(err) }) }),
        );
        break;
      }

      case "discardToken":
        this.executor?.discardToken(val.value.token);
        break;

      case "syncSecurity": {
        const { opId, networkGeneration, touch } = val.value;
        void this.executor?.syncSecurity(opId, networkGeneration, touch).then(
          (value) => !this.closed && this.emit({ case: "securitySnapshot", value }),
          (err) => !this.closed && this.emit({ case: "securitySnapshot", value: create(SecuritySnapshotEventSchema, { opId, networkGeneration, error: errorMessage(err) }) }),
        );
        break;
      }

      case "cacheRead": {
        const kind = val.value.kind;
        if (kind.case === "bootstrap") {
          const opId = kind.value.opId;
          void this.cacheWriteQueue
            .then(() => this.executor?.readBootstrapCache(opId))
            .then(
              (value) => !this.closed && value && this.emit({ case: "cacheBootstrapLoaded", value }),
              (err) => !this.closed && this.emit({ case: "cacheBootstrapLoaded", value: create(CacheBootstrapLoadedEventSchema, { opId, error: errorMessage(err) }) }),
            );
        } else if (kind.case === "events") {
          const { opId, sessionId, newest, afterSeq, limit } = kind.value;
          void this.cacheWriteQueue
            .then(() => this.executor?.readEventsCache(opId, sessionId, newest, afterSeq, limit))
            .then(
              (value) => !this.closed && value && this.emit({ case: "cacheEventsLoaded", value }),
              (err) => !this.closed && this.emit({ case: "cacheEventsLoaded", value: create(CacheEventsLoadedEventSchema, { opId, sessionId, error: errorMessage(err) }) }),
            );
        }
        break;
      }

      case "cacheWrite": {
        const writeEff = val.value;
        const prepared = this.executor?.prepareCacheWrite
          ? this.executor.prepareCacheWrite(writeEff)
          : () => this.executor?.writeCache(writeEff);
        if (prepared) this.cacheWriteQueue = this.cacheWriteQueue.then(() => prepared()).catch(() => undefined);
        break;
      }

      case "cacheDelete": {
        const delEff = val.value;
        this.cacheWriteQueue = this.cacheWriteQueue.then(() => this.executor?.deleteCache(delEff)).catch(() => undefined);
        break;
      }

      case "rpc":
        void this.executor?.executeRpc(val.value).then((ev) => !this.closed && this.dispatch(ev), () => undefined);
        break;

      case "dispatchCommand": {
        const cmdEff = val.value;
        void this.executor?.executeCommandAttempt(cmdEff).then(
          (value) => !this.closed && this.emit({ case: "commandOutcome", value }),
          (err) => {
            if (this.closed) return;
            const c = err as { code?: string; fromDaemon?: boolean; outcome?: string; message?: string };
            const value = c?.fromDaemon
              ? create(CommandOutcomeEventSchema, {
                  requestId: cmdEff.requestId,
                  attemptId: cmdEff.attemptId,
                  source: OutcomeSource.DAEMON,
                  signed: true,
                  hashesMatch: true,
                  attemptBound: true,
                  outerMatchesInner: true,
                  outerOutcome: c.outcome || "executed",
                  outerReason: c.code || "",
                  result: create(CommandResultSchema, { outcome: c.outcome || "executed", errorCode: c.code || "", errorMessage: c.message || "" }),
                })
              : create(CommandOutcomeEventSchema, {
                  requestId: cmdEff.requestId,
                  attemptId: cmdEff.attemptId,
                  source: OutcomeSource.LOCAL,
                  notForwarded: c?.outcome !== "unknown",
                  transportCode: c?.code || "failed_precondition",
                  transportDetail: errorMessage(err),
                });
            this.emit({ case: "commandOutcome", value });
          },
        );
        break;
      }

      case "saveHomeScope":
        writeStoredScope(val.value.scopeToken);
        break;

      case "attachmentsSettled":
        this.handleAttachmentsSettled(val.value.projectKey, val.value.consumed);
        break;

      case "navigate": {
        const nav = val.value;
        const replaceSession = nav.route === NavigateRoute.SESSION && !!this.pendingStart;
        if (nav.route === NavigateRoute.SESSION && nav.sessionId) {
          this.pendingStart = undefined;
        }
        if (nav.route === NavigateRoute.SESSION && !replaceSession && this.watchCounts.size === 0) {
          break;
        }
        this.navigateTo(nav.route, nav.projectKey, nav.sessionId, nav.machineId, replaceSession);
        break;
      }

      case "requestEntropy": {
        const minIds = Math.max(val.value.minIds, 16);
        const minFloats = Math.max(val.value.minFloats, 16);
        this.pendingEvents.push({
          ev: create(EventSchema, {
            nowMs: BigInt(Date.now()),
            monoMs: BigInt(Math.max(0, Math.floor(performance.now()))),
            event: {
              case: "entropyProvided",
              value: create(EntropyProvidedSchema, {
                randomIds: randomIds(minIds),
                unitFloats: randomFloats(minFloats),
              }),
            },
          }),
        });
        break;
      }

      case "sessionRestoreComposer": {
        const eff = val.value;
        const set = this.restoreComposerListeners.get(eff.sessionId);
        if (set) for (const fn of [...set]) fn(eff);
        break;
      }

      case "scheduleReadCache": {
        const { opId, machineId } = val.value;
        if (this.executor?.readScheduleCache) {
          void this.executor.readScheduleCache(opId, machineId).then(
            (value) => !this.closed && this.emit({ case: "scheduleCacheLoaded", value }),
            (err) =>
              !this.closed &&
              this.emit({
                case: "scheduleCacheLoaded",
                value: create(ScheduleCacheLoadedEventSchema, { opId, machineId, error: errorMessage(err) }),
              }),
          );
        } else {
          this.pendingEvents.push({
            ev: create(EventSchema, {
              nowMs: BigInt(Date.now()),
              monoMs: BigInt(Math.max(0, Math.floor(performance.now()))),
              event: {
                case: "scheduleCacheLoaded",
                value: create(ScheduleCacheLoadedEventSchema, { opId, machineId }),
              },
            }),
          });
        }
        break;
      }

      case "scheduleDeleteCache":
        void this.executor?.deleteScheduleCache?.(val.value.machineId)?.catch(() => undefined);
        break;

      case "scheduleLoadSeen": {
        const { machineId } = val.value;
        if (this.executor?.loadScheduleSeen) {
          void this.executor.loadScheduleSeen(machineId).then(
            (value) => !this.closed && this.emit({ case: "scheduleSeenLoaded", value }),
            (err) =>
              !this.closed &&
              this.emit({
                case: "scheduleSeenLoaded",
                value: create(ScheduleSeenLoadedEventSchema, { machineId, error: errorMessage(err) }),
              }),
          );
        } else {
          this.pendingEvents.push({
            ev: create(EventSchema, {
              nowMs: BigInt(Date.now()),
              monoMs: BigInt(Math.max(0, Math.floor(performance.now()))),
              event: {
                case: "scheduleSeenLoaded",
                value: create(ScheduleSeenLoadedEventSchema, { machineId, found: false }),
              },
            }),
          });
        }
        break;
      }

      case "scheduleSaveSeen":
        void this.executor?.saveScheduleSeen?.(val.value.machineId, val.value.tokens)?.catch(() => undefined);
        break;

      case "scheduleDismissNotifications":
        this.executor?.dismissScheduleNotifications?.(val.value.runs);
        break;

      case "scheduleNavigate": {
        const nav = val.value;
        switch (nav.route) {
          case ScheduleNavigateRoute.LIST:
            this.go?.("tasks");
            break;
          case ScheduleNavigateRoute.DETAIL:
            if (nav.machineId && nav.taskId) this.go?.(routeForTask(nav.machineId, nav.taskId));
            break;
          case ScheduleNavigateRoute.SESSION:
            if (nav.sessionId && (nav.runId || nav.firstSeq > 0n)) {
              this.scheduleFocus.set(nav.sessionId, { runId: nav.runId, seq: nav.firstSeq });
            }
            if (nav.sessionId) this.go?.(routeForSession(nav.sessionId));
            break;
          case ScheduleNavigateRoute.FORM_TIMING:
            for (const key of nav.formKey ? [nav.formKey, ""] : [""]) {
              const set = this.scheduleFormTimingListeners.get(key);
              if (set) for (const fn of [...set]) fn(nav.formKey);
            }
            break;
          case ScheduleNavigateRoute.CLOSE_FORM:
            if (nav.formKey) this.openFormKeys.delete(nav.formKey);
            for (const key of nav.formKey ? [nav.formKey, ""] : [""]) {
              const set = this.scheduleFormCloseListeners.get(key);
              if (set) for (const fn of [...set]) fn();
            }
            break;
          case ScheduleNavigateRoute.UNSPECIFIED:
            break;
        }
        break;
      }

      // Settings and machine screens still render in React on Web
      // and never send the events behind these effects.
      case "scheduleMachinePoll":
      case "directPreference":
      case "renameClient":
      case "checkUpdates":
      case "persistClientPrefs":
      case "prepareCleanupSession":
      case undefined:
        break;

      default: {
        const _exhaustive: never = val;
        void _exhaustive;
      }
    }
    this.onEffect?.(eff);
  }

  private handleAttachmentsSettled(projectKey: string, consumed: boolean) {
    const start = this.pendingStart;
    if (!consumed) {
      if (start?.projectKey === projectKey) this.pendingStart = undefined;
      return;
    }
    const tray = this.trays.get(projectKey)?.().tray;
    if (tray && start?.taken.length) tray.detach(start.taken);
    if (this.remote) {
      const draftKey = "new:" + projectKey;
      if ((this.remote.drafts.get(draftKey)?.trim() ?? "") === (start?.prompt ?? "")) {
        this.remote.drafts.delete(draftKey);
      }
      const keptDraft = this.remote.drafts.get(draftKey);
      if (keptDraft || (tray && tray.items.length > 0)) {
        this.pendingEvents.push({
          ev: create(EventSchema, {
            nowMs: BigInt(Date.now()),
            monoMs: BigInt(Math.max(0, Math.floor(performance.now()))),
            event: {
              case: "newSessionSetPrompt",
              value: create(NewSessionSetPromptActionSchema, {
                prompt: keptDraft ?? "",
                attachmentCount: tray?.items.length ?? 0,
                attachmentsMoved: false,
              }),
            },
          }),
        });
      }
      this.remote.touch();
    }
  }

  private navigateTo(route: NavigateRoute, projectKey: string, sessionId: string, machineId: string, replace = false) {
    if (!this.go) return;
    switch (route) {
      case NavigateRoute.SESSION:
        if (sessionId) this.go(routeForSession(sessionId), replace || undefined);
        break;
      case NavigateRoute.PROJECT:
        this.go(routeForProject(projectKey));
        break;
      case NavigateRoute.MACHINE_SETTINGS:
        if (machineId) this.go(routeForMachineSettings(machineId));
        break;
      case NavigateRoute.MACHINE_STATUS:
        if (machineId) this.go(routeForMachineStatus(machineId));
        break;
      case NavigateRoute.UNSPECIFIED:
        break;
    }
  }

  private invalidateTarget(target: ScreenTarget) {
    this.screenVersions.set(target, (this.screenVersions.get(target) ?? 0) + 1);
    if (target === ScreenTarget.HOME) this.homeCache = undefined;
    else if (target === ScreenTarget.PROJECT) this.projectCache = undefined;
    else if (target === ScreenTarget.SESSION) this.sessionCache.clear();
    else if (target === ScreenTarget.NEW_SESSION) this.newSessionCache = undefined;
    else if (target === ScreenTarget.SYNC_STATE) this.syncStateCache = undefined;
    else if (target === ScreenTarget.SCHEDULES) this.schedulesCache = undefined;
    else if (target === ScreenTarget.SCHEDULE_DETAIL) this.scheduleDetailCache = undefined;
    else if (target === ScreenTarget.SCHEDULE_FORM) this.scheduleFormCache.clear();
    else if (target === ScreenTarget.SCHEDULE_SESSION_STRIP) this.scheduleStripCache.clear();
  }

  private flushRenders() {
    if (this.dirtyTargets.size === 0) return;
    const targets = [...this.dirtyTargets];
    this.dirtyTargets.clear();

    if (targets.includes(ScreenTarget.SYNC_STATE) || targets.includes(ScreenTarget.HOME) || targets.includes(ScreenTarget.PROJECT)) {
      this.executor?.applySyncStateView(this.viewSyncState());
    }
    for (const target of targets) {
      const listeners = this.listeners.get(target);
      if (listeners) for (const fn of [...listeners]) fn();
    }
  }

  fireActiveTimer(kind: TimerKind, key = ""): boolean {
    const tk = timerKey(kind, key);
    const cur = this.timers.get(tk);
    if (!cur) return false;
    clearTimeout(cur.handle);
    this.timers.delete(tk);
    if (!this.closed) {
      this.dispatch(
        create(EventSchema, {
          event: {
            case: "timerFired",
            value: create(TimerFiredEventSchema, { timerId: cur.id, kind, key }),
          },
        }),
      );
    }
    return true;
  }

  private clearTimers() {
    for (const entry of this.timers.values()) clearTimeout(entry.handle);
    this.timers.clear();
  }

  dispose() {
    if (this.closed) return;
    this.stop();
    this.closed = true;
    this.pendingEvents = [];
    this.dirtyTargets.clear();
    for (const set of this.listeners.values()) set.clear();
    this.totalListeners = 0;
    this.trays.clear();
    this.scheduleFormCloseListeners.clear();
    this.scheduleFormTimingListeners.clear();
    this.scheduleFocus.clear();
    this.openFormKeys.clear();
    this.core.close();
  }
}

// --- React integration & singleton lookup ---

const hosts = new WeakMap<Remote, CoreHost>();

export function getCoreHost(remote: Remote): CoreHost {
  let host = hosts.get(remote);
  if (!host) {
    host = new CoreHost(remote);
    hosts.set(remote, host);
  }
  return host;
}

export function disposeCoreHost(remote: Remote): void {
  const host = hosts.get(remote);
  if (!host) return;
  hosts.delete(remote);
  host.dispose();
}

export function useCoreHost(remote: Remote, go?: Go): CoreHost {
  const host = getCoreHost(remote);
  useEffect(() => {
    if (go) host.setNavigator(go);
  }, [host, go]);
  return host;
}

export function useHomeView(host: CoreHost, endedLimit = 30): HomeViewModel {
  const subscribe = useCallback((fn: () => void) => host.subscribe(ScreenTarget.HOME, fn), [host]);
  const getSnapshot = useCallback(() => host.version(ScreenTarget.HOME), [host]);
  const version = useSyncExternalStore(subscribe, getSnapshot);
  return useMemo(() => host.viewHome(endedLimit), [host, version, endedLimit]);
}

export function useProjectView(host: CoreHost, projectKey: string, manage: boolean, endedLimit = 30): ProjectViewModel {
  const subscribe = useCallback((fn: () => void) => host.subscribe(ScreenTarget.PROJECT, fn), [host]);
  const getSnapshot = useCallback(() => host.version(ScreenTarget.PROJECT), [host]);
  const version = useSyncExternalStore(subscribe, getSnapshot);
  return useMemo(() => host.viewProject(projectKey, manage, endedLimit), [host, version, projectKey, manage, endedLimit]);
}

export function useNewSessionView(host: CoreHost, projectKey = ""): NewSessionViewModel {
  const subscribe = useCallback((fn: () => void) => host.subscribe(ScreenTarget.NEW_SESSION, fn), [host]);
  const getSnapshot = useCallback(() => host.version(ScreenTarget.NEW_SESSION), [host]);
  const version = useSyncExternalStore(subscribe, getSnapshot);
  return useMemo(() => host.viewNewSession(projectKey), [host, version, projectKey]);
}

/** Transitional Step 3/4 hook reading Core's SyncStateView. Delete once Settings/Schedules/Devices migrate into Core. */
export function useSyncState(host: CoreHost): SyncStateView {
  const subscribe = useCallback((fn: () => void) => host.subscribe(ScreenTarget.SYNC_STATE, fn), [host]);
  const getSnapshot = useCallback(() => host.version(ScreenTarget.SYNC_STATE), [host]);
  const version = useSyncExternalStore(subscribe, getSnapshot);
  return useMemo(() => host.viewSyncState(), [host, version]);
}

export function useSessionView(
  host: CoreHost,
  sessionId: string,
  window?: SessionWindowOptions,
  openOpts?: { refreshModels?: boolean; refreshSettings?: boolean },
): SessionViewModel {
  const refreshModels = !!openOpts?.refreshModels;
  const refreshSettings = !!openOpts?.refreshSettings;
  useEffect(() => {
    if (!sessionId) return;
    host.watchSession(sessionId);
    host.sessionOpen(sessionId, refreshModels, refreshSettings);
    return () => host.unwatchSession(sessionId);
  }, [host, sessionId, refreshModels, refreshSettings]);
  const subscribe = useCallback((fn: () => void) => host.subscribe(ScreenTarget.SESSION, fn), [host]);
  const getSnapshot = useCallback(() => host.version(ScreenTarget.SESSION), [host]);
  const version = useSyncExternalStore(subscribe, getSnapshot);
  const limit = window?.limit;
  const beforeSeq = window?.beforeSeq;
  const aroundSeq = window?.aroundSeq;
  const revealSeq = window?.revealSeq;
  const revealRunId = window?.revealRunId;
  const expandedKey = (window?.expandedGroupIds ?? []).join(",");
  return useMemo(
    () =>
      host.viewSession(sessionId, {
        limit,
        beforeSeq,
        aroundSeq,
        revealSeq,
        revealRunId,
        expandedGroupIds: expandedKey ? expandedKey.split(",") : [],
      }),
    [host, version, sessionId, limit, beforeSeq, aroundSeq, revealSeq, revealRunId, expandedKey],
  );
}

function useScreenVersion(host: CoreHost, target: ScreenTarget): number {
  const subscribe = useCallback((fn: () => void) => host.subscribe(target, fn), [host, target]);
  const getSnapshot = useCallback(() => host.version(target), [host, target]);
  return useSyncExternalStore(subscribe, getSnapshot);
}

export function useSchedulesView(host: CoreHost, endedLimit = 30): SchedulesViewModel {
  const version = useScreenVersion(host, ScreenTarget.SCHEDULES);
  return useMemo(() => host.viewSchedules(endedLimit), [host, version, endedLimit]);
}

export function useScheduleDetailView(host: CoreHost, machineId: string, taskId: string, earlierRunsLimit = 10): ScheduleDetailViewModel {
  const version = useScreenVersion(host, ScreenTarget.SCHEDULE_DETAIL);
  return useMemo(() => host.viewScheduleDetail(machineId, taskId, earlierRunsLimit), [host, version, machineId, taskId, earlierRunsLimit]);
}

export function useScheduleFormView(host: CoreHost, formKey = ""): ScheduleFormViewModel {
  const version = useScreenVersion(host, ScreenTarget.SCHEDULE_FORM);
  return useMemo(() => host.viewScheduleForm(formKey), [host, version, formKey]);
}

export function useScheduleSessionStripView(host: CoreHost, sessionId: string): ScheduleSessionStripViewModel {
  const version = useScreenVersion(host, ScreenTarget.SCHEDULE_SESSION_STRIP);
  return useMemo(() => host.viewScheduleSessionStrip(sessionId), [host, version, sessionId]);
}

export { ConnState };
