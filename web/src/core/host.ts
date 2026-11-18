import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { useCallback, useEffect, useMemo, useSyncExternalStore } from "react";
import type { Tray, TrayItem } from "../attach";
import { newRequestID } from "../crypto";
import * as l10n from "../i18n";
import { pushPrefs, pushState, pushSupported, setPushPrefs, type PushPrefs } from "../push";
import { getTheme, setTheme, type Theme } from "../theme";
import { checkUpdate } from "../update";
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
  type ViewModel,
} from "../gen/remoteagent/core/v1/app_pb";
import { ApprovalMode, type Agent } from "../gen/remoteagent/v1/event_pb";
import { ConnState, type HomeViewModel } from "../gen/remoteagent/core/v1/home_view_pb";
import { CommandResultSchema, type Command } from "../gen/remoteagent/v1/remote_pb";
import {
  MachineChartKind,
  MachinePollMode,
  MachineStatusCleanupEventSchema,
  MachineStatusPollTickEventSchema,
  MachineStatusRetryEventSchema,
  MachineStatusScreenRequestSchema,
  MachineStatusScrubEventSchema,
  MachineStatusTabEventSchema,
  MachineStatusWatchEventSchema,
  MachineUsageTab,
  QuotaScreenRequestSchema,
  type MachineStatusViewModel,
  type PrepareCleanupSessionEffect,
  type QuotaViewModel,
} from "../gen/remoteagent/core/v1/machine_view_pb";
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
import {
  CheckForUpdatesEventSchema,
  ClientDetailScreenRequestSchema,
  ClientUpdateStateEventSchema,
  ConnCheckStatusEventSchema,
  ConnProbeState,
  MachineDirectStatusEventSchema,
  MachineSettingsOpenEventSchema,
  MachineSettingsScreenRequestSchema,
  MachineSettingsSelectAgentEventSchema,
  NotificationPermissionState,
  PushRegistrationState,
  SetAppearancePrefEventSchema,
  SetNotificationPrefEventSchema,
  ThemePreference,
  UpdateClientNameEventSchema,
  UpdateDirectPreferenceEventSchema,
  UpdateMachineAgentDefaultsEventSchema,
  UpdateMachineDaemonSettingsEventSchema,
  type ClientDetailViewModel,
  type ClientUpdateStateEvent,
  type DevicesViewModel,
  type DirectRouteReport,
  type MachineSettingsViewModel,
  type SetNotificationPrefEvent,
  type SettingsViewModel,
} from "../gen/remoteagent/core/v1/settings_view_pb";
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
  pushDirectReport(machineId: string, report: DirectRouteReport): void;
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
  setWatchedSessions?(sessionIds: string[]): void;
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

function defaultRaceTimeout<T>(work: Promise<T>, ms: number): Promise<T | undefined> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  return Promise.race([
    work.finally(() => clearTimeout(timer)),
    new Promise<undefined>((resolve) => {
      timer = setTimeout(() => resolve(undefined), ms);
    }),
  ]);
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

const SETTINGS_TARGETS = [
  ScreenTarget.SETTINGS,
  ScreenTarget.MACHINE_STATUS,
  ScreenTarget.MACHINE_SETTINGS,
  ScreenTarget.QUOTA,
  ScreenTarget.DEVICES,
  ScreenTarget.CLIENT_DETAIL,
];

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
  ...SETTINGS_TARGETS,
];

export function toCoreTheme(t: Theme): ThemePreference {
  switch (t) {
    case "light": return ThemePreference.LIGHT;
    case "dark": return ThemePreference.DARK;
    default: return ThemePreference.AUTO;
  }
}

export function fromCoreTheme(t: ThemePreference): Theme {
  switch (t) {
    case ThemePreference.LIGHT: return "light";
    case ThemePreference.DARK: return "dark";
    default: return "auto";
  }
}

function readNotificationState(): {
  supported: boolean;
  permission: NotificationPermissionState;
  pushState: PushRegistrationState;
  prefs: PushPrefs;
} {
  const prefs = pushPrefs();
  let state: ReturnType<typeof pushState> = "unsupported";
  let supported = false;
  try {
    supported = pushSupported();
    state = typeof matchMedia === "function" ? pushState() : (supported && Notification.permission === "granted" ? "ok" : "unsupported");
  } catch {
    state = "unsupported";
  }
  const permission = state === "ios-browser"
    ? NotificationPermissionState.NEEDS_HOME_SCREEN
    : state === "unsupported"
      ? NotificationPermissionState.UNSUPPORTED
      : state === "denied"
        ? NotificationPermissionState.DENIED
        : state === "ok"
          ? NotificationPermissionState.GRANTED
          : NotificationPermissionState.UNSPECIFIED;
  const anyEnabled = prefs.attention || prefs.turns || prefs.scheduled !== false;
  const regState = permission === NotificationPermissionState.GRANTED && anyEnabled
    ? PushRegistrationState.ACTIVE
    : PushRegistrationState.DISABLED;
  return { supported, permission, pushState: regState, prefs };
}

function writeStoredPushPrefs(prefs: PushPrefs) {
  try {
    localStorage.setItem("ra-push", JSON.stringify(prefs));
  } catch { /* ignore storage failure */ }
}

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
  private settingsCache?: { version: number; vm: SettingsViewModel };
  private machineStatusCache = new Map<string, { version: number; vm: MachineStatusViewModel }>();
  private machineSettingsCache = new Map<string, { version: number; vm: MachineSettingsViewModel }>();
  private quotaCache = new Map<string, { version: number; vm: QuotaViewModel }>();
  private devicesCache?: { version: number; vm: DevicesViewModel };
  private clientDetailCache = new Map<string, { version: number; vm: ClientDetailViewModel }>();

  private started = false;
  private closed = false;
  private watchCounts = new Map<string, number>();
  private restoreComposerListeners = new Map<string, Set<(eff: SessionRestoreComposerEffect) => void>>();
  private scheduleFormCloseListeners = new Map<string, Set<() => void>>();
  private scheduleFormTimingListeners = new Map<string, Set<(formKey: string) => void>>();
  private scheduleFocus = new Map<string, { runId: string; seq: bigint }>();
  private openFormKeys = new Set<string>();
  private timers = new Map<string, { id: bigint; handle: ReturnType<typeof setTimeout> }>();
  private machinePollTimers = new Map<string, { generation: bigint; handle: ReturnType<typeof setTimeout> }>();
  private machineWatchCounts = new Map<string, { live: number; overview: number }>();
  private machineWatchActive = new Map<string, { visible: boolean; mode: MachinePollMode }>();
  private revertingDirect = new Set<string>();
  private syncingPrefs = false;
  private revertingPushPrefs = false;
  private lastPushPrefSig?: string;
  private lastPersistedPrefs?: PushPrefs;
  private directToggleErrors = new Map<string, string>();
  private clientRenameStates = new Map<string, { saving: boolean; error: string }>();
  private cleanupPickerListeners = new Set<(eff: PrepareCleanupSessionEffect) => void>();
  private extraListeners = new Set<() => void>();
  private extraVersion = 0;
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
  private bootstrapped = false;
  private preloadedBootstrap?: CacheBootstrapLoadedEvent;
  private readonly scheduleCacheReads = new Map<bigint, Promise<void>>();

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
        pushDirectReport: (machineId, report) => this.reportMachineDirectStatus(machineId, report),
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

  /** Bound startup cache reads so slow/blocked IndexedDB never stalls initial mount. */
  static readonly WARM_BOOTSTRAP_CACHE_TIMEOUT_MS = 150;
  /** Bound initial schedule cache hydration when opening on #/tasks. */
  static readonly WARM_SCHEDULE_CACHE_TIMEOUT_MS = 120;

  /** Reads IndexedDB bootstrap (and schedule cache on task routes) before first render
   * so sub-route refreshes never flash empty or missing-device states. */
  async warmLocalCache(
    route: string,
    raceTimeout: <T>(work: Promise<T>, ms: number) => Promise<T | undefined> = defaultRaceTimeout,
  ): Promise<void> {
    if (this.closed || !this.executor) return;
    if (!this.bootstrapped) {
      try {
        const boot = await raceTimeout(
          this.executor.readBootstrapCache(0n),
          CoreHost.WARM_BOOTSTRAP_CACHE_TIMEOUT_MS,
        );
        if (this.closed) return;
        if (boot && !this.bootstrapped) {
          this.preloadedBootstrap = boot;
          this.emit({ case: "cacheBootstrapLoaded", value: boot });
        }
      } catch {
        // Live sync will hydrate when connectivity starts.
      }
    }
    const [kind, ...rest] = route.split("/");
    if (kind === "tasks") {
      this.scheduleListOpen(true);
      const [mid, tid] = rest.map((part) => {
        try {
          return decodeURIComponent(part);
        } catch {
          return part;
        }
      });
      if (mid && tid && mid !== "new") {
        this.scheduleDetailOpen(mid, tid);
      }
      if (this.scheduleCacheReads.size > 0) {
        await raceTimeout(
          Promise.all([...this.scheduleCacheReads.values()]),
          CoreHost.WARM_SCHEDULE_CACHE_TIMEOUT_MS,
        );
      }
    }
  }

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
      this.executor?.setWatchedSessions?.([...this.watchCounts.keys()].reverse());
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
        this.executor?.setWatchedSessions?.([...this.watchCounts.keys()].reverse());
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

  // --- Settings, Machine & Devices Intents ---

  subscribeExtra(fn: () => void): () => void {
    this.extraListeners.add(fn);
    return () => { this.extraListeners.delete(fn); };
  }

  getExtraVersion(): number {
    return this.extraVersion;
  }

  private bumpExtra() {
    this.extraVersion++;
    for (const fn of [...this.extraListeners]) fn();
  }

  directError(machineId: string): string {
    return this.directToggleErrors.get(machineId) ?? "";
  }

  private setDirectError(machineId: string, error: string) {
    if ((this.directToggleErrors.get(machineId) ?? "") === error) return;
    if (error) this.directToggleErrors.set(machineId, error);
    else this.directToggleErrors.delete(machineId);
    this.bumpExtra();
  }

  clientRenameState(deviceId: string): { saving: boolean; error: string } {
    return this.clientRenameStates.get(deviceId) ?? { saving: false, error: "" };
  }

  private setClientRenameState(deviceId: string, next: { saving: boolean; error: string }) {
    const prev = this.clientRenameState(deviceId);
    if (prev.saving === next.saving && prev.error === next.error) return;
    if (!next.saving && !next.error) this.clientRenameStates.delete(deviceId);
    else this.clientRenameStates.set(deviceId, next);
    this.bumpExtra();
  }

  onCleanupPicker(fn: (eff: PrepareCleanupSessionEffect) => void): () => void {
    this.cleanupPickerListeners.add(fn);
    return () => { this.cleanupPickerListeners.delete(fn); };
  }

  private emitNotificationSync(patch: Omit<Partial<SetNotificationPrefEvent>, "$typeName">, revert = false) {
    if (revert) this.revertingPushPrefs = true;
    else this.syncingPrefs = true;
    try {
      this.emit({ case: "setNotificationPref", value: create(SetNotificationPrefEventSchema, patch) });
    } finally {
      if (revert) this.revertingPushPrefs = false;
      else this.syncingPrefs = false;
    }
  }

  syncBrowserPrefs() {
    if (this.closed) return;
    const ns = readNotificationState();
    this.syncingPrefs = true;
    try {
      this.lastPersistedPrefs = ns.prefs;
      this.lastPushPrefSig = `${ns.prefs.attention}:${ns.prefs.turns}:${ns.prefs.scheduled !== false}`;
      this.emit({ case: "setAppearancePref", value: create(SetAppearancePrefEventSchema, { theme: toCoreTheme(getTheme()) }) });
      this.emitNotificationSync({
        supported: ns.supported,
        permission: ns.permission,
        pushState: ns.pushState,
        notifyAttention: ns.prefs.attention,
        notifyTurnEnded: ns.prefs.turns,
        notifyScheduled: ns.prefs.scheduled !== false,
        errorDetail: "",
      });
    } finally {
      this.syncingPrefs = false;
    }
  }

  private syncMachineWatch(machineId: string) {
    if (!machineId || this.closed) return;
    const c = this.machineWatchCounts.get(machineId);
    const mode = c && c.live > 0 ? MachinePollMode.LIVE : c && c.overview > 0 ? MachinePollMode.OVERVIEW : MachinePollMode.OFF;
    const visible = mode !== MachinePollMode.OFF;
    const prev = this.machineWatchActive.get(machineId);
    if ((prev && prev.visible === visible && prev.mode === mode) || (!visible && !prev)) return;
    if (visible) this.machineWatchActive.set(machineId, { visible, mode });
    else this.machineWatchActive.delete(machineId);
    this.emit({ case: "machineStatusWatch", value: create(MachineStatusWatchEventSchema, { machineId, visible, mode }) });
  }

  watchMachineStatus(machineId: string, mode: MachinePollMode): () => void {
    if (!machineId || mode === MachinePollMode.OFF) return () => {};
    let entry = this.machineWatchCounts.get(machineId);
    if (!entry) {
      entry = { live: 0, overview: 0 };
      this.machineWatchCounts.set(machineId, entry);
    }
    if (mode === MachinePollMode.LIVE) entry.live++;
    else entry.overview++;
    this.syncMachineWatch(machineId);
    let released = false;
    return () => {
      if (released) return;
      released = true;
      const cur = this.machineWatchCounts.get(machineId);
      if (!cur) return;
      if (mode === MachinePollMode.LIVE) cur.live = Math.max(0, cur.live - 1);
      else cur.overview = Math.max(0, cur.overview - 1);
      if (cur.live === 0 && cur.overview === 0) this.machineWatchCounts.delete(machineId);
      this.syncMachineWatch(machineId);
    };
  }

  retryMachineStatus(machineId: string) {
    if (machineId && !this.closed) this.emit({ case: "machineStatusRetry", value: create(MachineStatusRetryEventSchema, { machineId }) });
  }

  selectMachineStatusTab(machineId: string, tab: MachineUsageTab) {
    if (machineId && !this.closed) this.emit({ case: "machineStatusTab", value: create(MachineStatusTabEventSchema, { machineId, tab }) });
  }

  scrubMachineStatus(machineId: string, kind: MachineChartKind, pointIndex: number, clear = false) {
    if (machineId && !this.closed) this.emit({ case: "machineStatusScrub", value: create(MachineStatusScrubEventSchema, { machineId, kind, pointIndex, clear }) });
  }

  cleanupMachineStatus(machineId: string) {
    if (machineId && !this.closed) this.emit({ case: "machineStatusCleanup", value: create(MachineStatusCleanupEventSchema, { machineId }) });
  }

  machineSettingsOpen(machineId: string) {
    if (machineId && !this.closed) this.emit({ case: "machineSettingsOpen", value: create(MachineSettingsOpenEventSchema, { machineId }) });
  }

  machineSettingsSelectAgent(machineId: string, agent: Agent) {
    if (machineId && !this.closed) this.emit({ case: "machineSettingsSelectAgent", value: create(MachineSettingsSelectAgentEventSchema, { machineId, agent }) });
  }

  updateMachineAgentDefaults(machineId: string, agent: Agent, patch: { model?: string; effort?: string; mode?: ApprovalMode }) {
    if (machineId && !this.closed) this.emit({ case: "updateMachineAgentDefaults", value: create(UpdateMachineAgentDefaultsEventSchema, { machineId, agent, ...patch }) });
  }

  updateMachineDaemonSettings(
    machineId: string,
    patch: { approvalTimeoutSeconds?: number; idleCloseMinutes?: number; imageUploadQuotaBytes?: bigint; cleanImages?: boolean },
  ) {
    if (machineId && !this.closed) this.emit({ case: "updateMachineDaemonSettings", value: create(UpdateMachineDaemonSettingsEventSchema, { machineId, ...patch, cleanImages: !!patch.cleanImages }) });
  }

  updateDirectPreference(machineId: string, enabled: boolean) {
    if (!machineId || this.closed) return;
    this.setDirectError(machineId, "");
    this.emit({ case: "updateDirectPreference", value: create(UpdateDirectPreferenceEventSchema, { machineId, enabled }) });
  }

  reportMachineDirectStatus(machineId: string, report: DirectRouteReport) {
    if (machineId && !this.closed) this.emit({ case: "machineDirectStatus", value: create(MachineDirectStatusEventSchema, { machineId, report }) });
  }

  updateClientName(deviceId: string, name: string, restoreDefault = false) {
    if (!deviceId || this.closed) return;
    this.setClientRenameState(deviceId, { saving: false, error: "" });
    this.emit({ case: "updateClientName", value: create(UpdateClientNameEventSchema, { deviceId, name, restoreDefault }) });
  }

  setNotificationPref(patch: Omit<Partial<SetNotificationPrefEvent>, "$typeName">) {
    if (!this.closed) this.emit({ case: "setNotificationPref", value: create(SetNotificationPrefEventSchema, patch) });
  }

  enablePushNotifications() {
    if (this.closed || !this.remote) return;
    const remote = this.remote;
    const prefs = this.lastPersistedPrefs ?? pushPrefs();
    this.setNotificationPref({ pushState: PushRegistrationState.REGISTERING, errorDetail: "" });
    void setPushPrefs(remote, prefs).then(
      () => {
        if (this.closed) return;
        const ns = readNotificationState();
        this.emitNotificationSync({ supported: ns.supported, permission: ns.permission, pushState: ns.pushState, errorDetail: "" });
      },
      (err) => {
        if (this.closed) return;
        const ns = readNotificationState();
        this.emitNotificationSync({ supported: ns.supported, permission: ns.permission, pushState: PushRegistrationState.FAILED, errorDetail: errorMessage(err) });
      },
    );
  }

  setAppearancePref(theme: ThemePreference) {
    if (!this.closed) this.emit({ case: "setAppearancePref", value: create(SetAppearancePrefEventSchema, { theme }) });
  }

  tickClock() {
    if (!this.closed) this.emit({ case: "clockUpdated", value: localCalendar() });
  }

  reportConnCheckStatus(serverState: ConnProbeState, latencyMs = 0, lastFailure = "") {
    if (!this.closed) this.emit({ case: "connCheckStatus", value: create(ConnCheckStatusEventSchema, { serverState, latencyMs, lastFailure }) });
  }

  reportClientUpdateState(ev: Omit<Partial<ClientUpdateStateEvent>, "$typeName">) {
    if (!this.closed) this.emit({ case: "clientUpdateState", value: create(ClientUpdateStateEventSchema, ev) });
  }

  checkForUpdates() {
    if (!this.closed) this.emit({ case: "checkForUpdates", value: create(CheckForUpdatesEventSchema) });
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

  private readView<T>(
    caseName: NonNullable<ViewModel["view"]["case"]>,
    screen: NonNullable<Parameters<typeof create<typeof ScreenSchema>>[1]>["screen"],
  ): T {
    const req = create(ScreenSchema, { screen });
    const vm = fromBinary(ViewModelSchema, this.core.view(toBinary(ScreenSchema, req)));
    if (vm.view.case !== caseName) throw new Error("unexpected view model case: " + vm.view.case);
    return vm.view.value as unknown as T;
  }

  viewSettings(): SettingsViewModel {
    const ver = this.version(ScreenTarget.SETTINGS);
    if (this.settingsCache?.version === ver) return this.settingsCache.vm;
    const vm = this.readView<SettingsViewModel>("settings", { case: "settings", value: {} });
    this.settingsCache = { version: ver, vm };
    return vm;
  }

  viewMachineStatus(machineId: string): MachineStatusViewModel {
    const ver = this.version(ScreenTarget.MACHINE_STATUS);
    const cached = this.machineStatusCache.get(machineId);
    if (cached?.version === ver) return cached.vm;
    const vm = this.readView<MachineStatusViewModel>("machineStatus", { case: "machineStatus", value: create(MachineStatusScreenRequestSchema, { machineId }) });
    this.machineStatusCache.set(machineId, { version: ver, vm });
    return vm;
  }

  viewMachineSettings(machineId: string): MachineSettingsViewModel {
    const ver = this.version(ScreenTarget.MACHINE_SETTINGS);
    const cached = this.machineSettingsCache.get(machineId);
    if (cached?.version === ver) return cached.vm;
    const vm = this.readView<MachineSettingsViewModel>("machineSettings", { case: "machineSettings", value: create(MachineSettingsScreenRequestSchema, { machineId }) });
    this.machineSettingsCache.set(machineId, { version: ver, vm });
    return vm;
  }

  viewQuota(machineId: string): QuotaViewModel {
    const ver = this.version(ScreenTarget.QUOTA);
    const cached = this.quotaCache.get(machineId);
    if (cached?.version === ver) return cached.vm;
    const vm = this.readView<QuotaViewModel>("quota", { case: "quota", value: create(QuotaScreenRequestSchema, { machineId }) });
    this.quotaCache.set(machineId, { version: ver, vm });
    return vm;
  }

  viewDevices(): DevicesViewModel {
    const ver = this.version(ScreenTarget.DEVICES);
    if (this.devicesCache?.version === ver) return this.devicesCache.vm;
    const vm = this.readView<DevicesViewModel>("devices", { case: "devices", value: {} });
    this.devicesCache = { version: ver, vm };
    return vm;
  }

  viewClientDetail(deviceId: string): ClientDetailViewModel {
    const ver = this.version(ScreenTarget.CLIENT_DETAIL);
    const cached = this.clientDetailCache.get(deviceId);
    if (cached?.version === ver) return cached.vm;
    const vm = this.readView<ClientDetailViewModel>("clientDetail", { case: "clientDetail", value: create(ClientDetailScreenRequestSchema, { deviceId }) });
    this.clientDetailCache.set(deviceId, { version: ver, vm });
    return vm;
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

    if (ev.event.case === "cacheBootstrapLoaded") {
      this.bootstrapped = true;
    }
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
      while ((this.pendingEvents.length > 0 || this.dirtyTargets.size > 0) && !this.closed) {
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
      }
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
          const boot = this.preloadedBootstrap;
          this.preloadedBootstrap = undefined;
          const read = boot
            ? Promise.resolve(create(CacheBootstrapLoadedEventSchema, { ...boot, opId }))
            : this.cacheWriteQueue.then(() => this.executor?.readBootstrapCache(opId));
          void read.then(
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
          const p = this.executor
            .readScheduleCache(opId, machineId)
            .then(
              (value) => {
                if (!this.closed) this.emit({ case: "scheduleCacheLoaded", value });
              },
              (err) => {
                if (!this.closed) {
                  this.emit({
                    case: "scheduleCacheLoaded",
                    value: create(ScheduleCacheLoadedEventSchema, { opId, machineId, error: errorMessage(err) }),
                  });
                }
              },
            )
            .finally(() => {
              this.scheduleCacheReads.delete(opId);
            });
          this.scheduleCacheReads.set(opId, p);
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

      case "scheduleMachinePoll": {
        const { machineId, delayMs, generation, cancel } = val.value;
        const prev = this.machinePollTimers.get(machineId);
        if (prev) {
          clearTimeout(prev.handle);
          this.machinePollTimers.delete(machineId);
        }
        if (!cancel && !this.closed) {
          const handle = setTimeout(() => {
            const cur = this.machinePollTimers.get(machineId);
            if (!cur || cur.generation !== generation) return;
            this.machinePollTimers.delete(machineId);
            if (!this.closed) {
              this.emit({
                case: "machineStatusPollTick",
                value: create(MachineStatusPollTickEventSchema, { machineId, generation }),
              });
            }
          }, Math.max(0, Number(delayMs)));
          this.machinePollTimers.set(machineId, { generation, handle });
        }
        break;
      }

      case "directPreference": {
        const { machineId, enabled } = val.value;
        if (this.revertingDirect.delete(machineId)) break;
        if (!this.remote) break;
        const remote = this.remote;
        const onFail = (err: unknown) => {
          if (this.closed) return;
          this.setDirectError(machineId, errorMessage(err));
          this.revertingDirect.add(machineId);
          this.emit({
            case: "updateDirectPreference",
            value: create(UpdateDirectPreferenceEventSchema, { machineId, enabled: !enabled }),
          });
        };
        try {
          const res: unknown = remote.direct.setEnabled(machineId, enabled);
          if (res && typeof (res as Promise<unknown>).catch === "function") {
            void (res as Promise<unknown>).catch(onFail);
          }
        } catch (err) {
          onFail(err);
        }
        break;
      }

      case "renameClient": {
        const { deviceId, name } = val.value;
        if (!this.remote) break;
        const remote = this.remote;
        this.setClientRenameState(deviceId, { saving: true, error: "" });
        void Promise.resolve()
          .then(() => remote.renameDevice(deviceId, name))
          .then(() => {
            if (!this.closed) this.setClientRenameState(deviceId, { saving: false, error: "" });
          })
          .catch((err) => {
            if (!this.closed) this.setClientRenameState(deviceId, { saving: false, error: errorMessage(err) });
          });
        break;
      }

      case "checkUpdates":
        void checkUpdate(true);
        break;

      case "persistClientPrefs": {
        const eff = val.value;
        if (eff.theme !== ThemePreference.UNSPECIFIED) {
          const nextTheme = fromCoreTheme(eff.theme);
          if (getTheme() !== nextTheme) setTheme(nextTheme);
        }
        const sig = `${eff.notifyAttention}:${eff.notifyTurnEnded}:${eff.notifyScheduled}`;
        if (!this.syncingPrefs && !this.revertingPushPrefs && this.lastPushPrefSig !== sig) {
          const prevPrefs = this.lastPersistedPrefs ?? pushPrefs();
          const nextPrefs: PushPrefs = {
            attention: eff.notifyAttention,
            turns: eff.notifyTurnEnded,
            scheduled: eff.notifyScheduled,
          };
          this.lastPushPrefSig = sig;
          this.lastPersistedPrefs = nextPrefs;
          writeStoredPushPrefs(nextPrefs);
          if (this.remote) {
            const remote = this.remote;
            void setPushPrefs(remote, nextPrefs)
              .then(() => {
                if (this.closed) return;
                const ns = readNotificationState();
                this.emitNotificationSync({ supported: ns.supported, permission: ns.permission, pushState: ns.pushState, errorDetail: "" });
              })
              .catch((err) => {
                if (this.closed) return;
                const ns = readNotificationState();
                this.lastPersistedPrefs = prevPrefs;
                this.lastPushPrefSig = `${prevPrefs.attention}:${prevPrefs.turns}:${prevPrefs.scheduled !== false}`;
                writeStoredPushPrefs(prevPrefs);
                this.emitNotificationSync({
                  supported: ns.supported,
                  permission: ns.permission,
                  pushState: PushRegistrationState.FAILED,
                  notifyAttention: prevPrefs.attention,
                  notifyTurnEnded: prevPrefs.turns,
                  notifyScheduled: prevPrefs.scheduled !== false,
                  errorDetail: errorMessage(err),
                }, true);
              });
          }
        }
        break;
      }

      case "prepareCleanupSession": {
        const eff = val.value;
        if (eff.needsDirectoryPicker) {
          for (const fn of [...this.cleanupPickerListeners]) fn(eff);
          break;
        }
        if (eff.projectKey && this.remote) {
          const prompt = l10n.t("machine_status.cleanup_prompt");
          const draft = this.remote.drafts.get("new:" + eff.projectKey);
          this.remote.drafts.set(
            "new:" + eff.projectKey,
            !draft ? prompt : draft.includes(prompt) ? draft : `${draft}\n\n${prompt}`,
          );
          if (eff.machineId) this.remote.draftMachines.set(eff.projectKey, eff.machineId);
          this.remote.focusProject = eff.projectKey;
          this.remote.touch();
          this.go?.(routeForProject(eff.projectKey));
        }
        break;
      }

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
    else if (target === ScreenTarget.SETTINGS) this.settingsCache = undefined;
    else if (target === ScreenTarget.MACHINE_STATUS) this.machineStatusCache.clear();
    else if (target === ScreenTarget.MACHINE_SETTINGS) this.machineSettingsCache.clear();
    else if (target === ScreenTarget.QUOTA) this.quotaCache.clear();
    else if (target === ScreenTarget.DEVICES) this.devicesCache = undefined;
    else if (target === ScreenTarget.CLIENT_DETAIL) this.clientDetailCache.clear();
  }

  private flushRenders() {
    if (this.dirtyTargets.size === 0) return;
    const targets = [...this.dirtyTargets];
    this.dirtyTargets.clear();

    if (targets.includes(ScreenTarget.SYNC_STATE) || targets.includes(ScreenTarget.HOME) || targets.includes(ScreenTarget.PROJECT)) {
      this.executor?.applySyncStateView(this.viewSyncState());
      for (const st of SETTINGS_TARGETS) {
        if (!targets.includes(st) && (this.listeners.get(st)?.size ?? 0) > 0) {
          this.invalidateTarget(st);
          targets.push(st);
        }
      }
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
    for (const entry of this.machinePollTimers.values()) clearTimeout(entry.handle);
    this.machinePollTimers.clear();
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
    this.cleanupPickerListeners.clear();
    this.extraListeners.clear();
    this.machineWatchCounts.clear();
    this.machineWatchActive.clear();
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

function useScreenVersion(host: CoreHost, target: ScreenTarget): number {
  const subscribe = useCallback((fn: () => void) => host.subscribe(target, fn), [host, target]);
  const getSnapshot = useCallback(() => host.version(target), [host, target]);
  return useSyncExternalStore(subscribe, getSnapshot);
}

function useExtraVersion(host: CoreHost): number {
  const subscribe = useCallback((fn: () => void) => host.subscribeExtra(fn), [host]);
  const getSnapshot = useCallback(() => host.getExtraVersion(), [host]);
  return useSyncExternalStore(subscribe, getSnapshot);
}

export function useHomeView(host: CoreHost, endedLimit = 30): HomeViewModel {
  const version = useScreenVersion(host, ScreenTarget.HOME);
  return useMemo(() => host.viewHome(endedLimit), [host, version, endedLimit]);
}

export function useProjectView(host: CoreHost, projectKey: string, manage: boolean, endedLimit = 30): ProjectViewModel {
  const version = useScreenVersion(host, ScreenTarget.PROJECT);
  return useMemo(() => host.viewProject(projectKey, manage, endedLimit), [host, version, projectKey, manage, endedLimit]);
}

export function useNewSessionView(host: CoreHost, projectKey = ""): NewSessionViewModel {
  const version = useScreenVersion(host, ScreenTarget.NEW_SESSION);
  return useMemo(() => host.viewNewSession(projectKey), [host, version, projectKey]);
}

export function useSyncState(host: CoreHost): SyncStateView {
  const version = useScreenVersion(host, ScreenTarget.SYNC_STATE);
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
  const version = useScreenVersion(host, ScreenTarget.SESSION);
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

export function useSettingsView(host: CoreHost): SettingsViewModel {
  const version = useScreenVersion(host, ScreenTarget.SETTINGS);
  return useMemo(() => host.viewSettings(), [host, version]);
}

export function useMachineStatusView(
  host: CoreHost,
  machineId: string,
  mode: MachinePollMode = MachinePollMode.LIVE,
): MachineStatusViewModel {
  useEffect(() => host.watchMachineStatus(machineId, mode), [host, machineId, mode]);
  const version = useScreenVersion(host, ScreenTarget.MACHINE_STATUS);
  return useMemo(() => host.viewMachineStatus(machineId), [host, version, machineId]);
}

export function useMachineSettingsView(host: CoreHost, machineId: string): MachineSettingsViewModel {
  useEffect(() => {
    if (machineId) host.machineSettingsOpen(machineId);
  }, [host, machineId]);
  const version = useScreenVersion(host, ScreenTarget.MACHINE_SETTINGS);
  return useMemo(() => host.viewMachineSettings(machineId), [host, version, machineId]);
}

export function useQuotaView(host: CoreHost, machineId: string): QuotaViewModel {
  const version = useScreenVersion(host, ScreenTarget.QUOTA);
  return useMemo(() => host.viewQuota(machineId), [host, version, machineId]);
}

export function useDevicesView(host: CoreHost): DevicesViewModel {
  const version = useScreenVersion(host, ScreenTarget.DEVICES);
  return useMemo(() => host.viewDevices(), [host, version]);
}

export function useClientDetailView(host: CoreHost, deviceId: string): ClientDetailViewModel {
  const version = useScreenVersion(host, ScreenTarget.CLIENT_DETAIL);
  return useMemo(() => host.viewClientDetail(deviceId), [host, version, deviceId]);
}

export function useDirectError(host: CoreHost, machineId: string): string {
  useExtraVersion(host);
  return host.directError(machineId);
}

export function useClientRenameState(host: CoreHost, deviceId: string): { saving: boolean; error: string } {
  const ver = useExtraVersion(host);
  return useMemo(() => host.clientRenameState(deviceId), [host, ver, deviceId]);
}

export {
  ConnProbeState,
  ConnState,
  MachineChartKind,
  MachinePollMode,
  MachineUsageTab,
  NotificationPermissionState,
  PushRegistrationState,
  ThemePreference,
};
