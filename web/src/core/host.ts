import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { useCallback, useEffect, useMemo, useSyncExternalStore } from "react";
import type { Tray, TrayItem } from "../attach";
import { newRequestID } from "../crypto";
import {
  CacheBootstrapLoadedEventSchema,
  CacheEventsLoadedEventSchema,
  CommandOutcomeEventSchema,
  EffectBatchSchema,
  EntropyProvidedSchema,
  EventSchema,
  FetchSessionActionSchema,
  HomeScreenRequestSchema,
  LocalCalendarSchema,
  MachineRouteStatusEventSchema,
  MonthStartSchema,
  NavigateRoute,
  NetworkChangedEventSchema,
  NewSessionScreenRequestSchema,
  NewSessionSetPromptActionSchema,
  NewSessionSubmitActionSchema,
  OutcomeSource,
  ProjectKeyActionSchema,
  ProjectScreenRequestSchema,
  RetrySessionEventsActionSchema,
  ScreenSchema,
  ScreenTarget,
  SecuritySnapshotEventSchema,
  SessionDataScreenRequestSchema,
  SessionPinEventSchema,
  SessionScheduleHintSchema,
  SessionScheduleHintsEventSchema,
  SessionWatchEventSchema,
  SyncLifecycleActionSchema,
  SyncLifecycleKind,
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
import { ConnState, type HomeViewModel } from "../gen/remoteagent/core/v1/home_view_pb";
import { CommandResultSchema } from "../gen/remoteagent/v1/remote_pb";
import type { NewSessionViewModel } from "../gen/remoteagent/core/v1/new_session_view_pb";
import type { ProjectViewModel } from "../gen/remoteagent/core/v1/project_view_pb";
import type { SessionDataView, SyncStateView } from "../gen/remoteagent/core/v1/sync_view_pb";
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
  applySessionDataView(sessionId: string, view: SessionDataView): void;
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

const ALL_TARGETS = [
  ScreenTarget.HOME,
  ScreenTarget.PROJECT,
  ScreenTarget.NEW_SESSION,
  ScreenTarget.SYNC_STATE,
  ScreenTarget.SESSION_DATA,
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
  private newSessionCache?: { version: number; key: string; vm: NewSessionViewModel };
  private syncStateCache?: { version: number; view: SyncStateView };
  private sessionDataCache?: { version: number; sessionId: string; view: SessionDataView };

  private started = false;
  private closed = false;
  private watchedSessionId = "";
  private watchCounts = new Map<string, number>();
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
        pushRouteStatus: (machineId, directReady) => {
          this.dispatch(
            create(EventSchema, {
              event: {
                case: "machineRouteStatus",
                value: create(MachineRouteStatusEventSchema, { machineId, directReady }),
              },
            }),
          );
        },
        pushScheduleHints: (hints) => {
          this.dispatch(
            create(EventSchema, {
              event: {
                case: "sessionScheduleHints",
                value: create(SessionScheduleHintsEventSchema, {
                  hints: hints.map((h) => create(SessionScheduleHintSchema, h)),
                }),
              },
            }),
          );
        },
        pushSessionPin: (sessionId, pinned) => {
          this.dispatch(
            create(EventSchema, {
              event: { case: "sessionPin", value: create(SessionPinEventSchema, { sessionId, pinned }) },
            }),
          );
        },
        changeProjectPref: (kind, path) => {
          if (kind === "pin" || kind === "unpin") {
            this.dispatch(
              create(EventSchema, {
                event: { case: "projectTogglePin", value: create(ProjectKeyActionSchema, { projectKey: path }) },
              }),
            );
          } else {
            this.dispatch(
              create(EventSchema, {
                event: { case: "projectToggleHide", value: create(ProjectKeyActionSchema, { projectKey: path }) },
              }),
            );
          }
        },
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
      if (!this.closed && this.started) {
        this.dispatch(create(EventSchema, { event: { case: "clockUpdated", value: localCalendar() } }));
      }
    }, CLOCK_TICK_MS);
    const available = this.remote ? this.remote.network : (typeof navigator === "undefined" || navigator.onLine !== false);
    this.dispatch(create(EventSchema, { event: { case: "clockUpdated", value: localCalendar() } }));
    this.dispatch(
      create(EventSchema, {
        event: {
          case: "syncLifecycle",
          value: create(SyncLifecycleActionSchema, {
            kind: SyncLifecycleKind.START,
            homeScopeToken: readStoredScope(),
            scopeHmacKey: this.explicitScopeHmacKey ?? new Uint8Array(),
          }),
        },
      }),
    );
    this.dispatch(
      create(EventSchema, {
        event: {
          case: "networkChanged",
          value: create(NetworkChangedEventSchema, { available, signature: String(available) }),
        },
      }),
    );
  }

  stop() {
    if (!this.started && this.timers.size === 0 && this.clockTimer === undefined) return;
    this.started = false;
    this.clearTimers();
    clearInterval(this.clockTimer);
    this.clockTimer = undefined;
    if (!this.closed) {
      this.dispatch(
        create(EventSchema, {
          event: {
            case: "syncLifecycle",
            value: create(SyncLifecycleActionSchema, { kind: SyncLifecycleKind.STOP }),
          },
        }),
      );
    }
  }

  setForeground(active: boolean) {
    if (this.closed) return;
    if (active) {
      this.dispatch(create(EventSchema, { event: { case: "clockUpdated", value: localCalendar() } }));
    }
    this.dispatch(
      create(EventSchema, {
        event: {
          case: "syncLifecycle",
          value: create(SyncLifecycleActionSchema, {
            kind: active ? SyncLifecycleKind.FOREGROUND : SyncLifecycleKind.BACKGROUND,
          }),
        },
      }),
    );
  }

  setNetwork(available: boolean, signature?: string) {
    if (this.closed) return;
    this.dispatch(
      create(EventSchema, {
        event: {
          case: "networkChanged",
          value: create(NetworkChangedEventSchema, {
            available,
            signature: signature ?? String(available),
          }),
        },
      }),
    );
  }

  reconnectNow() {
    if (this.closed) return;
    this.dispatch(
      create(EventSchema, {
        event: {
          case: "syncLifecycle",
          value: create(SyncLifecycleActionSchema, { kind: SyncLifecycleKind.RECONNECT_NOW }),
        },
      }),
    );
  }

  retryLogin() {
    if (this.closed) return;
    this.dispatch(
      create(EventSchema, {
        event: {
          case: "syncLifecycle",
          value: create(SyncLifecycleActionSchema, { kind: SyncLifecycleKind.RETRY_LOGIN }),
        },
      }),
    );
  }

  loadMoreSessions(): Promise<boolean> {
    this.dispatch(create(EventSchema, { event: { case: "homeLoadMoreEnded", value: { step: 50 } } }));
    return Promise.resolve(true);
  }

  async loadAllSessions(): Promise<void> {
    this.dispatch(create(EventSchema, { event: { case: "homeLoadMoreEnded", value: { step: 100 } } }));
  }

  retryList() {
    this.dispatch(create(EventSchema, { event: { case: "homeRetryLoad", value: {} } }));
  }

  retryPrefs() {
    if (this.lastProjectPrefEvent && this.viewProject("", true).manage?.saveError) {
      this.dispatch(this.lastProjectPrefEvent);
      return;
    }
    this.dispatch(create(EventSchema, { event: { case: "homeRetryLoad", value: {} } }));
  }

  ensureSession(sessionId: string): Promise<void> {
    if (!sessionId) return Promise.resolve();
    this.dispatch(
      create(EventSchema, {
        event: { case: "fetchSession", value: create(FetchSessionActionSchema, { sessionId }) },
      }),
    );
    return Promise.resolve();
  }

  retrySession(sessionId: string) {
    if (!sessionId) return;
    this.ensureSession(sessionId);
    this.retryEvents(sessionId);
  }

  retryEvents(sessionId: string) {
    if (!sessionId) return;
    this.dispatch(
      create(EventSchema, {
        event: { case: "retrySessionEvents", value: create(RetrySessionEventsActionSchema, { sessionId }) },
      }),
    );
  }

  watchSession(sessionId: string) {
    if (!sessionId) return;
    const prev = this.watchCounts.get(sessionId) ?? 0;
    this.watchCounts.set(sessionId, prev + 1);
    if (this.watchedSessionId !== sessionId) {
      if (this.watchedSessionId) {
        const oldSid = this.watchedSessionId;
        this.watchedSessionId = "";
        this.sessionDataCache = undefined;
        this.dispatch(
          create(EventSchema, {
            event: { case: "sessionWatch", value: create(SessionWatchEventSchema, { sessionId: oldSid, watched: false }) },
          }),
        );
      }
      this.watchedSessionId = sessionId;
      this.sessionDataCache = undefined;
      this.dispatch(
        create(EventSchema, {
          event: { case: "sessionWatch", value: create(SessionWatchEventSchema, { sessionId, watched: true }) },
        }),
      );
    }
  }

  unwatchSession(sessionId: string) {
    const next = (this.watchCounts.get(sessionId) ?? 0) - 1;
    if (next > 0) {
      this.watchCounts.set(sessionId, next);
      return;
    }
    this.watchCounts.delete(sessionId);
    if (this.watchedSessionId === sessionId) {
      this.watchedSessionId = "";
      this.sessionDataCache = undefined;
      this.dispatch(
        create(EventSchema, {
          event: { case: "sessionWatch", value: create(SessionWatchEventSchema, { sessionId, watched: false }) },
        }),
      );
    }
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
    const req = create(ScreenSchema, {
      screen: { case: "home", value: create(HomeScreenRequestSchema, { endedLimit }) },
    });
    const vm = fromBinary(ViewModelSchema, this.core.view(toBinary(ScreenSchema, req)));
    if (vm.view.case !== "home") throw new Error("unexpected view model case: " + vm.view.case);
    this.homeCache = { version: ver, endedLimit, vm: vm.view.value };
    return vm.view.value;
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
    const req = create(ScreenSchema, {
      screen: { case: "project", value: create(ProjectScreenRequestSchema, { projectKey, manage, endedLimit }) },
    });
    const vm = fromBinary(ViewModelSchema, this.core.view(toBinary(ScreenSchema, req)));
    if (vm.view.case !== "project") throw new Error("unexpected view model case: " + vm.view.case);
    this.projectCache = { version: ver, key: projectKey, manage, endedLimit, vm: vm.view.value };
    return vm.view.value;
  }

  viewNewSession(projectKey = ""): NewSessionViewModel {
    const ver = this.version(ScreenTarget.NEW_SESSION);
    if (this.newSessionCache && this.newSessionCache.version === ver && this.newSessionCache.key === projectKey) {
      return this.newSessionCache.vm;
    }
    const req = create(ScreenSchema, {
      screen: { case: "newSession", value: create(NewSessionScreenRequestSchema, { projectKey }) },
    });
    const vm = fromBinary(ViewModelSchema, this.core.view(toBinary(ScreenSchema, req)));
    if (vm.view.case !== "newSession") throw new Error("unexpected view model case: " + vm.view.case);
    this.newSessionCache = { version: ver, key: projectKey, vm: vm.view.value };
    return vm.view.value;
  }

  viewSyncState(): SyncStateView {
    const ver = this.version(ScreenTarget.SYNC_STATE);
    if (this.syncStateCache && this.syncStateCache.version === ver) {
      return this.syncStateCache.view;
    }
    const req = create(ScreenSchema, { screen: { case: "syncState", value: {} } });
    const vm = fromBinary(ViewModelSchema, this.core.view(toBinary(ScreenSchema, req)));
    if (vm.view.case !== "syncState") throw new Error("unexpected view model case: " + vm.view.case);
    this.syncStateCache = { version: ver, view: vm.view.value };
    return vm.view.value;
  }

  /** Reads SessionDataView ONLY for the currently watched session (never bulk-loads other sessions). */
  viewSessionData(sessionId: string): SessionDataView | undefined {
    if (!sessionId || sessionId !== this.watchedSessionId) return undefined;
    const ver = this.version(ScreenTarget.SESSION_DATA);
    if (
      this.sessionDataCache &&
      this.sessionDataCache.version === ver &&
      this.sessionDataCache.sessionId === sessionId
    ) {
      return this.sessionDataCache.view;
    }
    const req = create(ScreenSchema, {
      screen: { case: "sessionData", value: create(SessionDataScreenRequestSchema, { sessionId }) },
    });
    const vm = fromBinary(ViewModelSchema, this.core.view(toBinary(ScreenSchema, req)));
    if (vm.view.case !== "sessionData") throw new Error("unexpected view model case: " + vm.view.case);
    this.sessionDataCache = { version: ver, sessionId, view: vm.view.value };
    return vm.view.value;
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
        const ms = Math.max(0, Number(delayMs));
        const handle = setTimeout(() => {
          const cur = this.timers.get(tk);
          if (!cur || cur.id !== timerId) return;
          this.timers.delete(tk);
          if (!this.closed) {
            this.dispatch(
              create(EventSchema, {
                event: {
                  case: "timerFired",
                  value: create(TimerFiredEventSchema, { timerId, kind, key }),
                },
              }),
            );
          }
        }, ms);
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

      case "openStream": {
        const { legId, token } = val.value;
        this.executor?.openStreamLeg(legId, token, {
          onReady: (id, serverTimeMs) => {
            if (this.closed) return;
            this.dispatch(create(EventSchema, { event: { case: "streamReady", value: { legId: id, serverTimeMs } } }));
          },
          onFrame: (id, frame, onProcessed) => {
            if (this.closed) {
              onProcessed?.();
              return;
            }
            this.dispatch(create(EventSchema, { event: { case: "streamFrame", value: { legId: id, frame } } }), onProcessed);
          },
          onClosed: (id, info) => {
            if (this.closed) return;
            this.dispatch(create(EventSchema, { event: { case: "streamClosed", value: { legId: id, ...info } } }));
          },
        });
        break;
      }

      case "closeStream":
        this.executor?.closeStreamLeg(val.value.legId);
        break;

      case "ensureToken": {
        const { opId, forceLogin, timeoutMs } = val.value;
        void this.executor?.ensureToken(opId, forceLogin, timeoutMs).then(
          (res) => {
            if (!this.closed) this.dispatch(create(EventSchema, { event: { case: "tokenResult", value: res } }));
          },
          (err) => {
            if (!this.closed) {
              this.dispatch(
                create(EventSchema, {
                  event: {
                    case: "tokenResult",
                    value: create(TokenResultEventSchema, { opId, error: errorMessage(err) }),
                  },
                }),
              );
            }
          },
        );
        break;
      }

      case "discardToken":
        this.executor?.discardToken(val.value.token);
        break;

      case "syncSecurity": {
        const { opId, networkGeneration, touch } = val.value;
        void this.executor?.syncSecurity(opId, networkGeneration, touch).then(
          (snap) => {
            if (!this.closed) {
              this.dispatch(create(EventSchema, { event: { case: "securitySnapshot", value: snap } }));
            }
          },
          (err) => {
            if (!this.closed) {
              this.dispatch(
                create(EventSchema, {
                  event: {
                    case: "securitySnapshot",
                    value: create(SecuritySnapshotEventSchema, {
                      opId,
                      networkGeneration,
                      error: errorMessage(err),
                    }),
                  },
                }),
              );
            }
          },
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
              (res) => {
                if (!this.closed && res) {
                  this.dispatch(create(EventSchema, { event: { case: "cacheBootstrapLoaded", value: res } }));
                }
              },
              (err) => {
                if (!this.closed) {
                  this.dispatch(
                    create(EventSchema, {
                      event: {
                        case: "cacheBootstrapLoaded",
                        value: create(CacheBootstrapLoadedEventSchema, { opId, error: errorMessage(err) }),
                      },
                    }),
                  );
                }
              },
            );
        } else if (kind.case === "events") {
          const { opId, sessionId, newest, afterSeq, limit } = kind.value;
          void this.cacheWriteQueue
            .then(() => this.executor?.readEventsCache(opId, sessionId, newest, afterSeq, limit))
            .then(
              (res) => {
                if (!this.closed && res) {
                  this.dispatch(create(EventSchema, { event: { case: "cacheEventsLoaded", value: res } }));
                }
              },
              (err) => {
                if (!this.closed) {
                  this.dispatch(
                    create(EventSchema, {
                      event: {
                        case: "cacheEventsLoaded",
                        value: create(CacheEventsLoadedEventSchema, {
                          opId,
                          sessionId,
                          error: errorMessage(err),
                        }),
                      },
                    }),
                  );
                }
              },
            );
        }
        break;
      }

      case "cacheWrite": {
        const writeEff = val.value;
        const prepared = this.executor?.prepareCacheWrite
          ? this.executor.prepareCacheWrite(writeEff)
          : () => this.executor?.writeCache(writeEff);
        if (prepared) {
          this.cacheWriteQueue = this.cacheWriteQueue.then(() => prepared()).catch(() => undefined);
        }
        break;
      }

      case "cacheDelete": {
        const delEff = val.value;
        this.cacheWriteQueue = this.cacheWriteQueue
          .then(() => this.executor?.deleteCache(delEff))
          .catch(() => undefined);
        break;
      }

      case "rpc":
        void this.executor?.executeRpc(val.value).then(
          (ev) => {
            if (!this.closed) this.dispatch(ev);
          },
          () => undefined,
        );
        break;

      case "dispatchCommand": {
        const cmdEff = val.value;
        void this.executor?.executeCommandAttempt(cmdEff).then(
          (res) => {
            if (!this.closed) {
              this.dispatch(create(EventSchema, { event: { case: "commandOutcome", value: res } }));
            }
          },
          (err) => {
            if (!this.closed) {
              const cmdErr = err as { code?: string; fromDaemon?: boolean; outcome?: string; message?: string };
              const daemonOutcome = cmdErr?.fromDaemon
                ? create(CommandOutcomeEventSchema, {
                    requestId: cmdEff.requestId,
                    attemptId: cmdEff.attemptId,
                    source: OutcomeSource.DAEMON,
                    signed: true,
                    hashesMatch: true,
                    attemptBound: true,
                    outerMatchesInner: true,
                    outerOutcome: cmdErr.outcome || "executed",
                    outerReason: cmdErr.code || "",
                    result: create(CommandResultSchema, {
                      outcome: cmdErr.outcome || "executed",
                      errorCode: cmdErr.code || "",
                      errorMessage: cmdErr.message || "",
                    }),
                  })
                : create(CommandOutcomeEventSchema, {
                    requestId: cmdEff.requestId,
                    attemptId: cmdEff.attemptId,
                    source: OutcomeSource.LOCAL,
                    notForwarded: cmdErr?.outcome !== "unknown",
                    transportCode: cmdErr?.code || "failed_precondition",
                    transportDetail: errorMessage(err),
                  });
              this.dispatch(
                create(EventSchema, {
                  event: {
                    case: "commandOutcome",
                    value: daemonOutcome,
                  },
                }),
              );
            }
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
        if (nav.route === NavigateRoute.SESSION && nav.sessionId && this.remote) {
          this.remote.created.add(nav.sessionId);
          if (this.pendingStart?.prompt) this.remote.prompts.set(nav.sessionId, this.pendingStart.prompt);
          this.pendingStart = undefined;
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

      // Settings, machine, schedules, and session screens still render in React
      // on Web and never send the events behind these effects.
      case "scheduleMachinePoll":
      case "directPreference":
      case "renameClient":
      case "checkUpdates":
      case "persistClientPrefs":
      case "prepareCleanupSession":
      case "sessionRestoreComposer":
      case "scheduleReadCache":
      case "scheduleDeleteCache":
      case "scheduleLoadSeen":
      case "scheduleSaveSeen":
      case "scheduleDismissNotifications":
      case "scheduleNavigate":
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
    else if (target === ScreenTarget.NEW_SESSION) this.newSessionCache = undefined;
    else if (target === ScreenTarget.SYNC_STATE) this.syncStateCache = undefined;
    else if (target === ScreenTarget.SESSION_DATA) this.sessionDataCache = undefined;
  }

  private flushRenders() {
    if (this.dirtyTargets.size === 0) return;
    const targets = [...this.dirtyTargets];
    this.dirtyTargets.clear();

    if (targets.includes(ScreenTarget.SYNC_STATE) || targets.includes(ScreenTarget.HOME) || targets.includes(ScreenTarget.PROJECT)) {
      this.executor?.applySyncStateView(this.viewSyncState());
    }
    if (targets.includes(ScreenTarget.SESSION_DATA) && this.watchedSessionId) {
      const sd = this.viewSessionData(this.watchedSessionId);
      if (sd) this.executor?.applySessionDataView(this.watchedSessionId, sd);
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
    this.trays.clear();
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

/** Transitional Step 3 hook watching and reading SessionDataView for ONLY the open session. */
export function useSessionData(host: CoreHost, sessionId: string): SessionDataView | undefined {
  useEffect(() => {
    if (!sessionId) return;
    host.watchSession(sessionId);
    return () => host.unwatchSession(sessionId);
  }, [host, sessionId]);
  const subscribe = useCallback((fn: () => void) => host.subscribe(ScreenTarget.SESSION_DATA, fn), [host]);
  const getSnapshot = useCallback(() => host.version(ScreenTarget.SESSION_DATA), [host]);
  const version = useSyncExternalStore(subscribe, getSnapshot);
  return useMemo(() => host.viewSessionData(sessionId), [host, version, sessionId]);
}

export { ConnState };
