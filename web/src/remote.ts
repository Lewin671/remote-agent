import { DirectExecutor, peerFrameBody } from "./transport/direct-executor";
import { RESULT_RETENTION_MS } from "./result-retention.gen";
import { abortable, ownedRound, PathChanged } from "./transport/recovery";
import { cleanup } from "./cleanup";
import { Link as DeviceLink } from "./transport/link";
import { RelayPath } from "./transport/relay-path";
import type { FrameStream } from "./transport/path";
import { machineNotificationKinds } from "./machine-notifications";
import * as l10n from "./i18n";
import { UnsupportedHistoryError } from "./crypto";
import type { ImagePrepareFn } from "./attachment-executor";
import type { AttachmentStore } from "./attachment-store";
import {
  clearLoginToken,
  loadLoginToken,
  saveLoginToken,
  securityStoreV4,
  type SecurityStoreV4,
} from "./core/security-store-v4";
import {
  CommandRouteLeg,
  disposeCoreHost,
  getCoreHost,
  SecurityError,
  type CoreExecutor,
  type HostBridge,
  type StreamLegCallbacks,
} from "./core/host";
export { SecurityError };
export type { StreamLegCallbacks } from "./core/host";
import {
  SecurityClient,
  SecurityWorkflowError,
  createStandaloneSecurityClient,
  translateSecurityErrorMessage,
} from "./core/security-client";
import { renderText } from "./core/text";
import {
  AccountPrefsFetchedEventSchema,
  AccountPrefsPutResultEventSchema,
  CacheBootstrapLoadedEventSchema,
  CacheEventsLoadedEventSchema,
  CommandOutcomeEventSchema,
  CommandRouteHint,
  DevicesFetchedEventSchema,
  DirectEventsReceivedEventSchema,
  EventSchema as CoreEventSchema,
  EventsAppendedFrameSchema,
  EventsPageFetchedEventSchema,
  ListsFetchedEventSchema,
  OutcomeSource,
  SessionByIdFetchedEventSchema,
  SessionsPageFetchedEventSchema,
  TokenResultEventSchema,
  type CacheBootstrapLoadedEvent,
  type CacheDeleteEffect,
  type CacheEventsLoadedEvent,
  type CacheWriteEffect,
  type CommandOutcomeEvent,
  type DispatchCommandEffect,
  type Effect,
  type Event as CoreEvent,
  type RpcEffect,
  type TokenResultEvent,
} from "./gen/remoteagent/core/v1/app_pb";
import {
  SecurityBootstrapKind,
  SecurityErrorCode,
  SecurityKeyKind,
  SecurityOpenEnvelopeOpSchema,
  SecuritySealEnvelopeOpSchema,
  type SecurityHandoffCandidate,
  type SecurityPairingInfo,
  type SecurityStatusChangedEffect,
  type SecurityTokenUpdatedEffect,
  type SecurityWorkflowStatus,
} from "./gen/remoteagent/core/v1/security_pb";
import type {
  DirectDcReadStatsEffect,
  DirectPcAddCandidatesEffect,
  DirectPcCloseEffect,
  DirectPcConfirmEffect,
  DirectPcCreateEffect,
  DirectPcDescribeEffect,
  DirectPcSetRemoteEffect,
  DirectSignalSendEffect,
} from "./gen/remoteagent/core/v1/transport_pb";
import {
  ScheduleCacheLoadedEventSchema,
  ScheduleSeenLoadedEventSchema,
  ScheduleSeenTokenSchema,
  type ScheduleCacheLoadedEvent,
  type ScheduleNotificationRef,
  type ScheduleSeenLoadedEvent,
  type ScheduleSeenToken,
} from "./gen/remoteagent/core/v1/schedules_view_pb";
import { ConnState as CoreConnState } from "./gen/remoteagent/core/v1/home_view_pb";
import type { SyncMachineView, SyncSessionView, SyncStateView } from "./gen/remoteagent/core/v1/sync_view_pb";
import {
  type LiveSnapshot,
} from "./gen/remoteagent/v1/remote_pb";
import { machineName } from "./model";
import { DEFAULT_AGENT } from "./agents";
import type { SharedFileStore } from "./shared-file-store";
import { dispatchSecurityEffect, WebSecurityExecutor } from "./core/security-executor";

export const MAX_CLOCK_SKEW = 5 * 60 * 1000;

export enum Purpose {
  Event = 1,
  SessionMeta = 2,
  SessionKey = 3,
  MachineMeta = 4,
  Command = 5,
  Result = 6,
  TerminalOutput = 7,
  TerminalInput = 8,
  Notify = 10,
  Image = 11,
  Prefs = 12,
  DeviceMeta = 13,
  TerminalResize = 14,
  TerminalHistory = 15,
}

import {
  AuthService,
  ClientService,
  DeviceKind,
  ForwardResponseSchema,
  ListMachinesResponseSchema,
  ListRemoteSessionsResponseSchema,
  NotifyKind,
  PeerFrameSchema,
  ServerFrameSchema,
  StoredEventSchema,
  TermHelloSchema,
  AccountPrefsObjectSchema,
  type AccountPrefsObject,
  type Device,
  type Machine as RemoteMachine,
  type RemoteSession,
  type StoredEvent,
} from "./gen/remoteagent/v1/relay_pb";
import { PeerFrameResultSchema } from "./gen/remoteagent/v1/transport_pb";
import {
  create,
  fromBinary,
  toBinary,
  type MessageInitShape,
  type DescMessage,
  type MessageShape,
} from "@bufbuild/protobuf";
import {
  Code,
  ConnectError,
  createClient,
  type Client,
  type Interceptor,
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { relayFetch, relayEpoch } from "./transport";
import { closeScheduleNotification, restorePushPrefs, watchSecurityPush } from "./push";
import {
  checkServer,
  concat,
  newRequestID,
  utf8,
} from "./crypto";
import {
  EnvelopeSchema,
  type Envelope,
  type Header,
  PayloadKind,
} from "./gen/remoteagent/v1/envelope_pb";
import {
  Agent,
  ImageFormat,
} from "./gen/remoteagent/v1/event_pb";
import { matchesFormat, mimeOf } from "./imageview";
import {
  type DaemonSettings,
  type ListModelsResponse,
  type Session,
} from "./gen/remoteagent/v1/local_pb";
import {
  CommandResultSchema,
  CommandSchema,
  ImagePayloadSchema,
  ProjectPrefsSchema,
  TerminalFrameSchema,
  type CommandResult,
  type MachineMeta,
  type ProjectPrefs,
  type TerminalFrame,
} from "./gen/remoteagent/v1/remote_pb";
import { describeBrowser, type DeviceInfo } from "./devices";
import { CACHE_PAGE_SIZE, eventCache, type CachedEvent, type EventCache } from "./eventcache";
import { PEER_PROTOCOL_VERSION, RELAY_PROTOCOL_VERSION } from "./protocol";
import { type StoredAccount } from "./storage";

export type CommandKind = MessageInitShape<typeof CommandSchema>["kind"];

export interface MachineView { id: string; online: boolean; lastSeenMs: bigint; meta?: MachineMeta; metaRev: bigint; }
export interface SessionView { id: string; machineId: string; historyError?: string; info?: Session; checkpoint?: LiveSnapshot; checkpointStatus?: "ready" | "waiting" | "invalid"; metaRev: bigint; lastSeq: bigint; updatedMs: bigint; endedMs: bigint; }

// --- Transitional Step 3/4 command helper (remove when Session migrates to Core) ---

export const COMMAND_TTL_MS = 60_000;
export const COMMAND_EXPIRY_SKEW_MS = MAX_CLOCK_SKEW;
export type Outcome = "not_sent" | "executed" | "conflict" | "rejected" | "unknown";
export interface PreparedPayload { machineId: string; sid: string; plain: Uint8Array; }

export class RequestSlot {
  readonly evidence: { readonly unresolved?: boolean; includePrior: (prior: boolean) => void; trackHandoff: () => () => void; };
  beforeDispatch?: (slot: RequestSlot) => Promise<void>;
  constructor(public requestId: string, public deadline: number, public prepared?: PreparedPayload, prior = false) {
    let priorFlag = prior, handoffs = 0;
    this.evidence = {
      get unresolved(): boolean | undefined { return priorFlag || handoffs > 0 ? true : undefined; },
      includePrior(p: boolean) { if (p) priorFlag = true; },
      trackHandoff() {
        handoffs++;
        let undone = false;
        return () => { if (!undone) { undone = true; handoffs = Math.max(0, handoffs - 1); } };
      },
    };
  }
}

export const newSlot = (now = Date.now()) => new RequestSlot(newRequestID(), now + COMMAND_TTL_MS, undefined, false);

export class CommandError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly fromDaemon = false,
    readonly readOnly = false,
    readonly outcome: Outcome = fromDaemon ? "unknown" : "not_sent",
    readonly conflictKind = "",
    readonly resultAvailable = true,
  ) {
    super(
      (outcome === "executed" && !resultAvailable) || conflictKind === "identity"
        ? message
        : code === "disk_full" ? l10n.t("machine_status.disk_full") : code === "storage_exhausted" ? l10n.t("machine_status.storage_exhausted") : message,
    );
  }
  get needsResultRefresh() {
    return (this.outcome === "executed" && !this.resultAvailable) || (this.outcome === "conflict" && this.conflictKind === "identity");
  }
}

export function uncertain(e: unknown): boolean {
  if (e instanceof CommandError) return e.outcome === "unknown" || e.needsResultRefresh;
  return e instanceof ConnectError;
}

export function apiBase(server: string): string {
  const origin = checkServer(server);
  return import.meta.env.VITE_SAME_ORIGIN_API ? location.origin : origin;
}

export interface TermConn { send(frame: MessageInitShape<typeof TerminalFrameSchema>): void; close(): void; }
export type ConnState = "connecting" | "online" | "offline";
export type Link = "online" | "waiting" | "no-network" | "no-server";

export function readOnlyCommand(cmd: CommandKind): boolean {
  return !!cmd && (
    ["getSettings", "listModels", "listDirectories", "getLiveSnapshot", "getMachineStatus", "getUsageStats", "getImageUploadStatus", "getSharedFileChunk"].includes(cmd.case ?? "") ||
    (cmd.case === "schedule" && ["list", "get"].includes(cmd.value.operation ?? ""))
  );
}

const EMPTY_PREFS: ReadonlyMap<string, string> = new Map();
const SYNCING_PREFS: ReadonlyMap<string, string> = new Map([["", "syncing"]]);

const IMAGE_CACHE = 40;
const OPEN_TIMEOUT_MS = 15_000;
const RPC_TIMEOUT_MS = 45_000;
const SETUP_IDLE_MS = RPC_TIMEOUT_MS + OPEN_TIMEOUT_MS;
const SESSION_PAGE = 50;
export { SESSION_PAGE };
export const PREFS_OBJECT = "project-prefs";
const SCOPE_STORAGE_KEY = "ra-scope";
const SCOPE_HMAC_KEY_VALUE = "scope-hmac-key";

export type ImageResult = { kind: "ok"; blob: Blob; width: number; height: number; format: ImageFormat } | { kind: "expired" } | { kind: "missing" } | { kind: "bad"; why?: string };
export interface PrefsState { status: "loading" | "ready" | "error"; error: string; snap?: { revision: bigint; prefs: ProjectPrefs }; pending: ReadonlyMap<string, string>; failed?: { op: { id: string; kind: string; path: string }; error: string }; changedAt?: number; }
const emptyPrefs = () => create(ProjectPrefsSchema, { schema: 1 });

const PAYLOAD_PURPOSES: Partial<Record<PayloadKind, Purpose>> = {
  [PayloadKind.EVENT]: Purpose.Event,
  [PayloadKind.SESSION_META]: Purpose.SessionMeta,
  [PayloadKind.SESSION_KEY]: Purpose.SessionKey,
  [PayloadKind.MACHINE_META]: Purpose.MachineMeta,
  [PayloadKind.COMMAND]: Purpose.Command,
  [PayloadKind.COMMAND_RESULT]: Purpose.Result,
  [PayloadKind.TERMINAL_OUTPUT]: Purpose.TerminalOutput,
  [PayloadKind.TERMINAL_INPUT]: Purpose.TerminalInput,
  [PayloadKind.IMAGE]: Purpose.Image,
  [PayloadKind.NOTIFY]: Purpose.Notify,
  [PayloadKind.ACCOUNT_PREFS]: Purpose.Prefs,
  [PayloadKind.DEVICE_META]: Purpose.DeviceMeta,
};

function purposeOf(kind: PayloadKind): Purpose {
  return PAYLOAD_PURPOSES[kind] ?? Purpose.Event;
}

type CacheWritePayload = { kind: "machine"; raw: RemoteMachine } | { kind: "session"; raw: RemoteSession } | { kind: "events"; sessionId: string; raw: CachedEvent[] } | { kind: "prefs"; raw: AccountPrefsObject } | { kind: "sessionOpen"; sessionId: string; open: boolean };

const CONNECT_CODES: Partial<Record<Code, string>> = {
  [Code.DeadlineExceeded]: "deadline_exceeded",
  [Code.FailedPrecondition]: "failed_precondition",
  [Code.PermissionDenied]: "permission_denied",
  [Code.InvalidArgument]: "invalid_argument",
  [Code.NotFound]: "not_found",
  [Code.Unauthenticated]: "unauthenticated",
  [Code.Unavailable]: "unavailable",
  [Code.Unknown]: "unknown",
  [Code.Internal]: "internal",
  [Code.Canceled]: "canceled",
  [Code.Aborted]: "aborted",
  [Code.AlreadyExists]: "already_exists",
  [Code.ResourceExhausted]: "resource_exhausted",
};
const connectErrorCode = (code: Code) => CONNECT_CODES[code] ?? "unknown";

function readStoredScopeToken(): string {
  try {
    const raw = localStorage.getItem(SCOPE_STORAGE_KEY) ?? "";
    return !raw || raw === "*" ? "all" : raw;
  } catch { return "all"; }
}

function syncSessionToView(sv: SyncSessionView): SessionView {
  return {
    id: sv.sessionId, machineId: sv.machineId, info: sv.info, checkpoint: sv.checkpoint,
    checkpointStatus: sv.checkpointInvalid ? "invalid" : sv.checkpoint ? "ready" : "waiting",
    metaRev: sv.metaRev, lastSeq: sv.lastSeq, updatedMs: sv.updatedMs, endedMs: sv.listedEndedMs,
    ...(sv.retiredHistory ? { historyError: l10n.t("errors.history_keys_unavailable_records_from_before_recovery_cannot_be") }
      : sv.openFailed ? { historyError: l10n.t("session.history_unreadable") } : {}),
  };
}

export class Remote implements CoreExecutor {
  private hostBridge?: HostBridge;
  private syncStateView?: SyncStateView;

  private persistedMachines = new Map<string, RemoteMachine>();
  private persistedSessions = new Map<string, RemoteSession>();
  private persistedOpen = new Set<string>();

  private watchedSessionIds: string[] = [];
  private describingSelf = false;
  private devicesPushSeq = 1n;
  private devicesSyncWaiters: Array<() => void> = [];
  private serverAhead = false;
  private securityStatusSnapshot?: SecurityWorkflowStatus;
  authorityDeviceId = "";
  clockWarning = "";
  authorityError = "";

  directExec: DirectExecutor;
  readonly sharedFileStore?: SharedFileStore;
  readonly attachmentStore?: AttachmentStore;
  prepareImageOverride?: ImagePrepareFn;
  private readonly secStore: SecurityStoreV4;
  private readonly secExec: WebSecurityExecutor;
  readonly deviceLink: DeviceLink;

  protected api: Client<typeof ClientService>;
  private unwatchSecurityPush?: () => void;
  private securityTimer?: ReturnType<typeof setInterval>;
  private foreground = true;
  private token = "";
  private tokenExpires = 0;
  private tokenWarmed = false;
  private loggingIn?: Promise<void>;
  private loginController?: AbortController;
  private tokenWrites: Promise<void> = Promise.resolve();
  private networkGeneration = 0;
  private verifiedNetworkGeneration = 0;
  private wakeSecurity?: Promise<void>;
  private wakeController?: AbortController;
  private wakeRequired = false;
  private wakeRevision = 0;
  private closed = false;
  private legs = new Map<bigint, { ws: FrameStream; stopped: boolean }>();
  private listeners = new Set<() => void>();
  private version = 0;

  network = typeof navigator === "undefined" || navigator.onLine !== false;
  removalVerified = false;
  devicesStatus: "loading" | "ready" | "error" = "loading";
  focusProject = "";
  readonly draftMachines = new Map<string, string>();
  readonly termFirst = new Set<string>();
  readonly scroll = new Map<string, { top: number; stick: boolean }>();
  readonly approvalReasons = new Map<string, string>();
  readonly notices = new Map<string, string>();

  private cachedMachineMap?: Map<string, MachineView>;
  private cachedSessionMap?: Map<string, SessionView>;
  private cachedDeviceMap?: Map<string, DeviceInfo>;
  private imageCache = new Map<string, ImageResult>();
  private models = new Map<string, Promise<ListModelsResponse>>();
  private modelReads = new Set<string>();
  private settingsCache = new Map<string, Promise<DaemonSettings | undefined>>();

  protected constructor(
    readonly account: StoredAccount,
    private cache?: EventCache,
    initialStatus?: SecurityWorkflowStatus,
    protected readonly historyPageSize = 500,
    protected readonly sessionPageSize = SESSION_PAGE,
    store: SecurityStoreV4 = securityStoreV4,
    fetchFn: typeof fetch = relayFetch,
  ) {
    this.secStore = store;
    if (initialStatus) {
      this.applySecurityStatusSnapshot(initialStatus);
    }
    const auth: Interceptor = (next) => (req) => {
      const sentToken = this.token;
      req.header.set("Authorization", "Bearer " + sentToken);
      return next(req).catch((e: unknown) => {
        if (e instanceof ConnectError && e.code === Code.Unauthenticated && this.token === sentToken) {
          this.discardToken(sentToken);
        }
        throw e;
      });
    };
    this.api = createClient(
      ClientService,
      createConnectTransport({
        fetch: fetchFn,
        baseUrl: apiBase(account.server),
        useBinaryFormat: true,
        defaultTimeoutMs: RPC_TIMEOUT_MS,
        interceptors: [auth],
      }),
    );
    this.secExec = new WebSecurityExecutor({
      store,
      fetch: fetchFn,
      emit: (ev) => this.hostBridge?.dispatchEvent(ev),
    });
    this.deviceLink = new DeviceLink(
      new RelayPath(
        (id) => {
          const sm = this.syncMachine(id);
          const direct = !!sm?.directReady;
          const authorized = !this.removed && this.isMachineAuthorized(id);
          return {
            relayRequired: !direct,
            unlinked: this.unlinked,
            reachable: direct || (authorized && !!sm?.online),
            ready: direct || this.conn === "online",
            authorized,
            protocolVersion: direct ? PEER_PROTOCOL_VERSION : (sm?.protocolVersion || sm?.meta?.protocolVersion),
            capabilities: sm?.meta?.capabilities,
          };
        },
        () => this.relayEpoch(),
        (request, options) => this.api.forward(request, options),
        (signal) => this.login(signal),
      ),
    );
    this.directExec = this.newDirectExecutor();
  }

  static async load(initialStatus?: SecurityWorkflowStatus): Promise<Remote | undefined> {
    let status = initialStatus;
    if (!status) {
      const standalone = createStandaloneSecurityClient();
      try {
        status = await standalone.client.bootstrap();
        if (status.bootstrap === SecurityBootstrapKind.PENDING_REGISTRATION) {
          const resumed = await standalone.client.resumePendingOp();
          if (resumed.status) status = resumed.status;
        }
      } finally {
        standalone.dispose();
      }
    }
    if (
      !status ||
      (status.bootstrap !== SecurityBootstrapKind.READY &&
        status.bootstrap !== SecurityBootstrapKind.REMOVED) ||
      !status.server ||
      !status.deviceId
    ) {
      return undefined;
    }
    const acct: StoredAccount = {
      server: status.server,
      deviceId: status.deviceId,
      accountId: status.accountId,
    };
    const r = new Remote(acct, eventCache(), status);
    return r;
  }

  attachHost(bridge: HostBridge) {
    this.hostBridge = bridge;
  }

  private ensureHost(): HostBridge {
    if (!this.hostBridge) {
      getCoreHost(this);
    }
    return this.hostBridge!;
  }

  // --- Host bridge delegation for remaining Step 3-6 callers ---

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  snapshot = () => this.version;

  touch() {
    this.changed();
  }

  protected changed(_immediate = false) {
    this.version++;
    cleanup(
      () => this.deviceLink.changed(),
      ...[...this.listeners],
    );
  }

  private pairingTimer?: ReturnType<typeof setInterval>;
  private pairingBusy = false;
  private started = false;

  private newDirectExecutor() {
    return new DirectExecutor({
      emit: (ev) => this.hostBridge?.dispatchEvent(ev),
      onPushedEvent: (machineId, pcId, env, header, size) => {
        this.handleDirectEvent(machineId, pcId, env, header, size);
      },
    });
  }

  start() {
    this.started = true;
    this.closed = false;
    // stop() closes the executor for good, so a page that was hidden and came
    // back needs a fresh one; otherwise every handshake is silently dropped.
    if (this.directExec.isClosed) this.directExec = this.newDirectExecutor();
    this.unwatchSecurityPush?.();
    this.unwatchSecurityPush = watchSecurityPush(() => {
      if (!this.closed) {
        void this.ensureHost().securityClient.syncNow(true, this.token).catch(() => undefined);
      }
    });
    clearInterval(this.securityTimer);
    this.securityTimer = setInterval(() => {
      if (!this.closed && this.foreground && this.conn === "online") {
        void this.ensureHost().securityClient.syncNow(false, this.token).catch(() => undefined);
      }
    }, 12_000);
    this.ensureHost().start();
    void this.watchPairingDelivery();
  }

  stop() {
    this.closed = true;
    this.directExec.close();
    while (this.devicesSyncWaiters.length) this.devicesSyncWaiters.shift()!();
    this.unwatchSecurityPush?.();
    this.unwatchSecurityPush = undefined;
    clearInterval(this.securityTimer);
    this.securityTimer = undefined;
    this.stopPairingDelivery();
    this.deviceLink.closeStreams(l10n.t("errors.terminal_connection_closed"));
    this.retireNetworkIO();
    for (const legId of [...this.legs.keys()]) this.closeStreamLeg(legId);
    this.hostBridge?.stop();
  }

  dispose() {
    this.stop();
    this.secExec.close();
    disposeCoreHost(this);
    this.hostBridge = undefined;
  }

  private applySecurityStatusSnapshot(status: SecurityWorkflowStatus) {
    const prevRemoved = this.removed;
    this.securityStatusSnapshot = status;
    if (status.isAuthority && status.deviceId) this.authorityDeviceId = status.deviceId;
    this.clockWarning = status.clockSkewed ? l10n.t("errors.this_device_s_clock_is_behind_the_account_state") : "";
    if (status.removed && !this.removalVerified) {
      this.removalVerified = true;
      this.discardToken();
      if (!this.closed) this.stop();
    } else if (status.pendingOp?.hasDelivery && !this.closed && this.started) {
      void this.watchPairingDelivery();
    } else if (!status.pendingOp?.hasDelivery) {
      this.stopPairingDelivery();
    }
    if (this.removed !== prevRemoved || this.hostBridge) {
      this.changed();
    }
  }

  private async watchPairingDelivery() {
    if (this.closed || !this.securityStatusSnapshot?.pendingOp?.hasDelivery) {
      this.stopPairingDelivery();
      return;
    }
    this.pairingTimer ??= setInterval(() => void this.continuePairing(), 1500);
  }

  private stopPairingDelivery() {
    clearInterval(this.pairingTimer);
    this.pairingTimer = undefined;
  }

  private async continuePairing() {
    if (this.closed || this.pairingBusy || (typeof document !== "undefined" && document.hidden)) return;
    this.pairingBusy = true;
    try {
      if (!this.securityStatusSnapshot?.pendingOp?.hasDelivery) {
        this.stopPairingDelivery();
        return;
      }
      await this.ensureHost().securityClient.resumePendingOp();
      if (!this.securityStatusSnapshot?.pendingOp?.hasDelivery) this.stopPairingDelivery();
      this.authorityError = "";
      if (!this.closed) await this.loadDevices(false);
    } catch (e) {
      this.authorityError = errorText(e);
      if (
        (e instanceof SecurityWorkflowError &&
          [
            SecurityErrorCode.INVALID,
            SecurityErrorCode.UNTRUSTED,
            SecurityErrorCode.CONFLICT,
            SecurityErrorCode.STORAGE,
            SecurityErrorCode.CAPACITY,
            SecurityErrorCode.CANCELED,
          ].includes(e.code)) ||
        (e instanceof ConnectError &&
          [Code.InvalidArgument, Code.NotFound, Code.PermissionDenied, Code.Unauthenticated, Code.FailedPrecondition].includes(e.code))
      ) {
        this.stopPairingDelivery();
      }
    } finally {
      this.pairingBusy = false;
      if (!this.closed) this.changed();
    }
  }

  setForeground(active: boolean) {
    this.foreground = active;
    this.ensureHost().setForeground(active);
  }

  setNetwork(available: boolean, signature?: string) {
    this.network = available;
    this.ensureHost().setNetwork(available, signature);
    this.changed(true);
  }

  networkChanged(signature: string) {
    this.ensureHost().setNetwork(this.network, signature);
  }

  reconnectNow(networkChanged = false) {
    this.ensureHost().reconnectNow(networkChanged);
  }

  retryLogin() {
    this.ensureHost().retryLogin();
  }

  loadMoreSessions(): Promise<boolean> {
    return this.ensureHost().loadMoreSessions();
  }

  loadAllSessions(): Promise<void> {
    return this.ensureHost().loadAllSessions();
  }

  retryList() {
    this.ensureHost().retryList();
  }

  retryPrefs() {
    this.ensureHost().retryPrefs();
  }

  ensureSession(id: string) {
    return this.ensureHost().ensureSession(id);
  }

  retrySession(id: string) {
    this.ensureHost().retrySession(id);
  }

  retryEvents(id: string) {
    this.ensureHost().retryEvents(id);
  }

  setWatchedSessions(sessionIds: string[]) {
    this.watchedSessionIds = [...sessionIds];
  }

  private handleDirectEvent(machineId: string, pcId: bigint, env: Envelope, header: Header, _size: number) {
    if (this.closed) return;
    const sid = header.sessionId;
    if (!sid || header.seq <= 0n || !this.watchedSessionIds.includes(sid)) return;
    this.hostBridge?.dispatchEvent(
      create(CoreEventSchema, {
        event: {
          case: "directEventsReceived",
          value: create(DirectEventsReceivedEventSchema, {
            sessionId: sid,
            machineId,
            pcId,
            sender: machineId,
            wireEnvelopes: [env],
          }),
        },
      }),
    );
  }

  changePrefs(kind: "pin" | "unpin" | "hide" | "unhide", path: string) {
    this.ensureHost().changeProjectPref(kind, path);
    if (!this.syncStateView?.prefsSyncing) {
      return Promise.resolve(true);
    }
    return new Promise<boolean>((resolve) => {
      const unsub = this.subscribe(() => {
        if (!this.syncStateView?.prefsSyncing) {
          unsub();
          resolve(true);
        }
      });
    });
  }

  // --- SyncStateView projection ---

  private syncMachine(id: string): SyncMachineView | undefined {
    return this.syncStateView?.machines.find((m) => m.machineId === id);
  }

  private syncSession(id: string): SyncSessionView | undefined {
    return this.syncStateView?.sessions.find((s) => s.sessionId === id);
  }

  private isMachineAuthorized(id: string): boolean {
    const v = this.syncStateView;
    if (!v || !v.securityLoaded) return true;
    const dev = v.devices.find((d) => d.deviceId === id);
    return dev ? dev.trusted : v.devices.length === 0;
  }

  applySyncStateView(view: SyncStateView) {
    const wasOnline = this.conn === "online";
    this.syncStateView = view;
    this.cachedMachineMap = undefined;
    this.cachedSessionMap = undefined;
    this.cachedDeviceMap = undefined;
    if (view.authorityDeviceId) this.authorityDeviceId = view.authorityDeviceId;
    this.clockWarning = view.accountClockSkewed || this.securityStatusSnapshot?.clockSkewed
      ? l10n.t("errors.this_device_s_clock_is_behind_the_account_state")
      : "";
    if (view.securityLoaded) {
      for (const pm of view.machines) {
        if (!this.isMachineAuthorized(pm.machineId)) {
          void this.keepValue("schedules:" + pm.machineId, undefined);
        }
      }
    }
    if (view.devices.length > 0 || view.machinesListed) this.devicesStatus = "ready";
    while (this.devicesSyncWaiters.length) this.devicesSyncWaiters.shift()!();
    this.changed();
    if (!wasOnline && (this.conn as ConnState) === "online" && !this.closed) void restorePushPrefs(this);
  }

  get conn(): ConnState {
    if (this.removed) return "offline";
    return this.syncStateView?.connection === CoreConnState.CONNECTED ? "online" : this.syncStateView?.connection === CoreConnState.OFFLINE ? "offline" : "connecting";
  }
  get connError(): string { return renderText(this.syncStateView?.connectionReason); }
  get failures(): number { return this.syncStateView?.connectionReason ? 1 : 0; }
  get connStep(): string { return renderText(this.syncStateView?.connectionStep); }
  get connStartedAt(): number | undefined { return undefined; }
  get everOnline(): boolean { return this.syncStateView?.liveLoaded ?? false; }
  get removed(): boolean { return Boolean(this.syncStateView?.removed || this.securityStatusSnapshot?.removed || this.removalVerified); }
  get link(): Link {
    if (!this.network) return "no-network";
    const v = this.syncStateView;
    if (!v) return "waiting";
    if (v.removed || v.connection === CoreConnState.OFFLINE || (v.unlinked && !v.reachabilityUnknown)) return "no-server";
    return v.connection === CoreConnState.CONNECTED ? "online" : "waiting";
  }
  get displayGrace(): boolean { return Boolean(this.syncStateView?.displayGrace); }
  get reachabilityUnknown(): boolean { return Boolean(this.syncStateView?.reachabilityUnknown); }
  get showWaiting(): boolean { return !this.displayGrace && this.conn !== "online" && this.link === "waiting"; }
  get unlinked(): boolean { return !this.reachabilityUnknown && (this.link === "no-network" || this.link === "no-server" || (this.link === "waiting" && !this.listed)); }
  get lostAt(): number | undefined { const ms = Number(this.syncStateView?.disconnectedAtMs ?? 0n); return ms > 0 ? ms : undefined; }
  get listed(): boolean { return !!(this.syncStateView?.machinesListed && this.syncStateView?.sessionsListed); }
  get listError(): string { return renderText(this.syncStateView?.sessionListError); }
  get moreSessions(): boolean { return this.syncStateView?.hasMoreSessions ?? false; }
  get machines(): ReadonlyMap<string, MachineView> {
    if (!this.cachedMachineMap) {
      const m = new Map<string, MachineView>();
      for (const pm of this.syncStateView?.machines ?? []) {
        const authorized = this.isMachineAuthorized(pm.machineId);
        m.set(pm.machineId, { id: pm.machineId, online: authorized && pm.online, lastSeenMs: pm.lastSeenMs, meta: pm.meta, metaRev: pm.metaRev });
      }
      this.cachedMachineMap = m;
    }
    return this.cachedMachineMap;
  }
  get sessions(): ReadonlyMap<string, SessionView> {
    if (!this.cachedSessionMap) {
      const m = new Map<string, SessionView>();
      for (const sv of this.syncStateView?.sessions ?? []) m.set(sv.sessionId, syncSessionToView(sv));
      this.cachedSessionMap = m;
    }
    return this.cachedSessionMap;
  }
  get devices(): ReadonlyMap<string, DeviceInfo> {
    if (!this.cachedDeviceMap) {
      const v = this.syncStateView;
      const m = new Map<string, DeviceInfo>();
      for (const dv of v?.devices ?? []) {
        if (v?.securityLoaded && !dv.trusted) continue;
        m.set(dv.deviceId, { id: dv.deviceId, kind: dv.kind, pairingIncomplete: dv.createdMs === 0n && !dv.meta, createdMs: dv.createdMs, lastSeenMs: dv.lastSeenMs, meta: dv.meta, metaRev: dv.metaRev });
      }
      this.cachedDeviceMap = m;
    }
    return this.cachedDeviceMap;
  }
  deviceKind(id: string): DeviceKind | undefined {
    return this.syncStateView?.devices.find((d) => d.deviceId === id)?.kind;
  }
  get prefs(): PrefsState {
    const v = this.syncStateView;
    return { status: (v?.prefsLoadFailed || v?.prefsBroken) && !v.prefs ? "error" : v?.prefsLoaded ? "ready" : "loading", error: "", snap: v?.prefs ? { revision: v.prefsRevision, prefs: v.prefs } : undefined, pending: v?.prefsSyncing ? SYNCING_PREFS : EMPTY_PREFS };
  }
  projectPrefs(): ProjectPrefs { return this.syncStateView?.prefs ?? emptyPrefs(); }
  get securityStatus(): SecurityWorkflowStatus | undefined { return this.securityStatusSnapshot; }
  get securityClient(): SecurityClient { return this.ensureHost().securityClient; }
  get recoveryStatusStale() { return this.securityStatusSnapshot?.recoveryStatusStale ?? false; }
  demoFlag(_name: string): boolean { return false; }
  peer(machineId: string | undefined) { return this.deviceLink.status(machineId ?? ""); }

  // --- CoreExecutor: Stream / Auth / Security / Cache / RPC ---

  openStreamLeg(legId: bigint, token: string, callbacks: StreamLegCallbacks) {
    this.closeStreamLeg(legId);
    const ws = this.deviceLink.openPathStream(apiBase(this.account.server).replace(/^http/, "ws") + "/v1/stream");
    const state = { ws, stopped: false };
    this.legs.set(legId, state);
    let processing = Promise.resolve(), queuedBytes = 0, queuedFrames = 0;

    const drop = (error: string, code?: number) => {
      if (state.stopped) return;
      state.stopped = true;
      if (this.legs.get(legId) === state) this.legs.delete(legId);
      this.directExec.detachLeg(ws);
      ws.close();
      callbacks.onClosed(legId, { policyViolation: code === 1008, errorCode: code === 1008 ? "unauthenticated" : "", error, unresolvedHost: false });
    };

    ws.onOpen = () => {
      if (state.stopped || this.legs.get(legId) !== state) return;
      try { ws.send(toBinary(PeerFrameSchema, create(PeerFrameSchema, { kind: { case: "hello", value: { token } } })) as Uint8Array<ArrayBuffer>); }
      catch (e) { drop(errorText(e)); }
    };
    ws.onError = () => drop(l10n.t("errors.disconnected_from_server"));
    ws.onEnd = (code) => drop(l10n.t("errors.disconnected_from_server"), code);
    ws.onFrame = (data) => {
      if (state.stopped || this.legs.get(legId) !== state) return;
      if (data.byteLength > 2 * 1024 * 1024) { drop(l10n.t("errors.push_backlog_reconnecting")); return; }
      let frame;
      try { frame = fromBinary(ServerFrameSchema, new Uint8Array(data)); }
      catch (e) { drop(errorText(e)); return; }
      if (frame.kind.case === "ready") {
        this.serverAhead = BigInt(Date.now()) - frame.kind.value.serverTimeMs > BigInt(MAX_CLOCK_SKEW);
        this.directExec.attachLeg(ws);
        callbacks.onReady(legId, frame.kind.value.serverTimeMs);
        return;
      }
      if (frame.kind.case === "peerFrameDelivery" || frame.kind.case === "peerFrameResult") {
        const raw = peerFrameBody(new Uint8Array(data), frame.kind.case === "peerFrameDelivery" ? 11 : 12);
        if (raw) {
          if (frame.kind.case === "peerFrameDelivery") {
            this.hostBridge?.dispatchDirectSignal(raw);
          } else {
            try {
              const res = fromBinary(PeerFrameResultSchema, raw);
              this.hostBridge?.dispatchDirectSignalDelivery(new Uint8Array(res.frameId), res.status, Number(res.retryAfterMs));
            } catch { /* Ignore malformed receipt. */ }
          }
        }
        return;
      }
      queuedBytes += data.byteLength;
      queuedFrames++;
      if (queuedBytes > 8 * 1024 * 1024 || queuedFrames > 1024) { drop(l10n.t("errors.push_backlog_reconnecting")); return; }
      processing = processing.then(async () => {
        if (state.stopped || this.legs.get(legId) !== state) return;
        const mapped = await this.mapStreamFrame(frame.kind);
        if (mapped) {
          callbacks.onFrame(legId, mapped);
        }
      }).catch((e) => {
        if (!state.stopped && this.legs.get(legId) === state) drop(errorText(e));
      }).finally(() => { queuedBytes -= data.byteLength; queuedFrames--; });
    };
  }

  closeStreamLeg(legId: bigint) {
    const leg = this.legs.get(legId);
    if (!leg) return;
    leg.stopped = true;
    this.legs.delete(legId);
    this.directExec.detachLeg(leg.ws);
    leg.ws.close();
  }

  private mapStreamFrame(kind: ReturnType<typeof fromBinary<typeof ServerFrameSchema>>["kind"]) {
    switch (kind.case) {
      case "heartbeat": return { case: "heartbeat" as const, value: true };
      case "machineUpdated": return kind.value.machine ? { case: "wireMachineUpdated" as const, value: kind.value.machine } : undefined;
      case "machineRemoved": return { case: "machineRemovedId" as const, value: kind.value.machineId };
      case "sessionUpdated": return kind.value.session ? { case: "wireSessionUpdated" as const, value: kind.value.session } : undefined;
      case "eventsAppended": return { case: "eventsAppended" as const, value: create(EventsAppendedFrameSchema, { sessionId: kind.value.sessionId, wireEvents: kind.value.events }) };
      case "accountPrefsUpdated": return kind.value.object ? { case: "wireAccountPrefsUpdated" as const, value: kind.value.object } : undefined;
      case "deviceUpdated": return kind.value.device ? { case: "wireDeviceUpdated" as const, value: kind.value.device } : undefined;
      case "deviceRemoved": return { case: "deviceRemovedId" as const, value: kind.value.deviceId };
      default: return undefined;
    }
  }

  private async warmTokenOnce() {
    if (this.tokenWarmed) return;
    this.tokenWarmed = true;
    try {
      const saved = await loadLoginToken(this.account.server, this.account.deviceId);
      if (saved && !this.token) { this.token = saved.token; this.tokenExpires = saved.expiresMs; }
    } catch { /* Ignore missing token. */ }
  }

  async ensureToken(opId: bigint, forceLogin: boolean, timeoutMs: bigint): Promise<TokenResultEvent> {
    await this.warmTokenOnce();
    if (!forceLogin && this.token && this.tokenExpires >= Date.now() + 86_400_000) {
      return create(TokenResultEventSchema, { opId, token: this.token, expiresMs: BigInt(this.tokenExpires) });
    }
    this.discardToken(this.token);
    await this.tokenWrites;
    const stop = new AbortController();
    const timer = setTimeout(() => stop.abort(new ConnectError(l10n.t("errors.connection_timed_out"), Code.DeadlineExceeded)), timeoutMs > 0n ? Number(timeoutMs) : this.roundBudget());
    try {
      await this.login(stop.signal);
      if (this.closed) { this.token = ""; this.tokenExpires = 0; return create(TokenResultEventSchema, { opId, errorCode: "canceled", error: "stopped" }); }
      return create(TokenResultEventSchema, { opId, token: this.token, expiresMs: BigInt(this.tokenExpires) });
    } catch (e) {
      return create(TokenResultEventSchema, { opId, removed: e instanceof ConnectError && e.code === Code.PermissionDenied, errorCode: e instanceof ConnectError ? connectErrorCode(e.code) : "error", error: errorText(e) });
    } finally { clearTimeout(timer); }
  }

  discardToken(token?: string) {
    if (token !== undefined && token !== "" && this.token && this.token !== token) return;
    this.token = "";
    this.tokenExpires = 0;
    this.tokenWrites = this.tokenWrites.then(() => clearLoginToken(this.account.server, this.account.deviceId)).catch(() => undefined);
  }

  securityConfig(): { account: string; deviceId: string; server?: string } | undefined {
    return {
      account: this.account.accountId ?? "",
      deviceId: this.account.deviceId,
      server: this.account.server,
    };
  }

  securityEffect(eff: Effect["effect"]) {
    dispatchSecurityEffect(this.secExec, eff);
  }

  securityTokenUpdated(eff: SecurityTokenUpdatedEffect) {
    if (eff.cleared) {
      this.discardToken();
      return;
    }
    this.token = eff.token;
    this.tokenExpires = Number(eff.expiresMs);
    this.tokenWrites = this.tokenWrites
      .then(() => saveLoginToken(this.account.server, this.account.deviceId, { token: eff.token, expiresMs: Number(eff.expiresMs) }))
      .catch(() => undefined);
  }

  securityStatusChanged(eff: SecurityStatusChangedEffect) {
    if (eff.status) {
      this.applySecurityStatusSnapshot(eff.status);
    }
  }

  async readBootstrapCache(opId: bigint): Promise<CacheBootstrapLoadedEvent> {
    const homeScopeToken = readStoredScopeToken();
    let scopeHmacKey = new Uint8Array(32);
    const wireMachines: RemoteMachine[] = [], wireSessions: RemoteSession[] = [];
    let wirePrefs: AccountPrefsObject | undefined;
    if (!this.cache) { crypto.getRandomValues(scopeHmacKey); return create(CacheBootstrapLoadedEventSchema, { opId, homeScopeToken, scopeHmacKey }); }
    try {
      const existingKey = await this.cache.get(SCOPE_HMAC_KEY_VALUE);
      if (existingKey instanceof Uint8Array && existingKey.length === 32) scopeHmacKey = new Uint8Array(existingKey);
      else { crypto.getRandomValues(scopeHmacKey); await this.cache.set(SCOPE_HMAC_KEY_VALUE, scopeHmacKey); }

      const [keptMachines, keptSessions, keptPrefs, keptOpen] = await Promise.all([
        this.cache.get("machines"), this.cache.get("sessions"), this.cache.get("prefs"), this.cache.get("open-sessions"),
      ]);
      const valid = (v: unknown): v is Record<string, unknown> =>
        !!v && typeof v === "object" && (v as { deviceId?: string }).deviceId === this.account.deviceId && (v as { server?: string }).server === this.account.server;

      if (valid(keptOpen) && Array.isArray(keptOpen.ids)) for (const id of keptOpen.ids) if (typeof id === "string") this.persistedOpen.add(id);
      if (valid(keptMachines) && keptMachines.bytes instanceof Uint8Array) {
        for (const m of fromBinary(ListMachinesResponseSchema, keptMachines.bytes).machines) {
          this.persistedMachines.set(m.id, m);
          wireMachines.push(m);
        }
      }
      if (valid(keptSessions) && keptSessions.bytes instanceof Uint8Array) {
        for (const rs of fromBinary(ListRemoteSessionsResponseSchema, keptSessions.bytes).sessions) {
          this.persistedSessions.set(rs.sessionId, rs);
          wireSessions.push(rs);
        }
      }
      if (valid(keptPrefs) && keptPrefs.bytes instanceof Uint8Array) {
        wirePrefs = fromBinary(AccountPrefsObjectSchema, keptPrefs.bytes);
      }
      return create(CacheBootstrapLoadedEventSchema, { opId, wireMachines, wireSessions, wirePrefs, homeScopeToken, scopeHmacKey });
    } catch (e) {
      return create(CacheBootstrapLoadedEventSchema, { opId, wireMachines, wireSessions, wirePrefs, homeScopeToken, scopeHmacKey, error: errorText(e) });
    }
  }

  async readEventsCache(opId: bigint, sessionId: string, newest: boolean, afterSeq: bigint, limit: number): Promise<CacheEventsLoadedEvent> {
    if (!this.cache) return create(CacheEventsLoadedEventSchema, { opId, sessionId });
    try {
      const cap = limit > 0 ? limit : CACHE_PAGE_SIZE;
      const rows = newest && this.cache.readNewest ? await this.cache.readNewest(sessionId, cap) : await this.cache.read(sessionId, afterSeq, cap);
      const knownLast = this.syncSession(sessionId)?.lastSeq;
      if (newest && knownLast !== undefined && rows.length && rows[rows.length - 1].seq > knownLast) {
        return create(CacheEventsLoadedEventSchema, { opId, sessionId, broken: true });
      }
      const wireEvents: StoredEvent[] = rows.map((row) =>
        create(StoredEventSchema, { seq: row.seq, ...(row.envelope ? { envelope: row.envelope } : {}) }),
      );
      return create(CacheEventsLoadedEventSchema, { opId, sessionId, wireEvents });
    } catch (e) {
      return create(CacheEventsLoadedEventSchema, { opId, sessionId, error: errorText(e) });
    }
  }

  private extractCacheWrite(eff: CacheWriteEffect): CacheWritePayload | undefined {
    switch (eff.kind.case) {
      case "writeMachine": return { kind: "machine", raw: eff.kind.value };
      case "writeSession":
      case "keepSessionOwner": return { kind: "session", raw: eff.kind.value };
      case "writeStoredEvents": {
        if (!this.cache) return undefined;
        const raw: CachedEvent[] = eff.kind.value.events.flatMap((se) =>
          se.envelope ? [{ seq: se.seq, envelope: se.envelope }] : [],
        );
        return raw.length > 0 ? { kind: "events", sessionId: eff.kind.value.sessionId, raw } : undefined;
      }
      case "writePrefs": return { kind: "prefs", raw: eff.kind.value };
      case "setSessionOpen": return { kind: "sessionOpen", sessionId: eff.kind.value.sessionId, open: eff.kind.value.open };
      default: return undefined;
    }
  }

  prepareCacheWrite(eff: CacheWriteEffect): (() => Promise<void>) | undefined {
    const payload = this.extractCacheWrite(eff);
    return payload ? () => this.writeCachePayload(payload) : undefined;
  }

  async writeCache(eff: CacheWriteEffect): Promise<void> {
    const payload = this.extractCacheWrite(eff);
    if (payload) await this.writeCachePayload(payload);
  }

  private async writeCachePayload(payload: CacheWritePayload): Promise<void> {
    switch (payload.kind) {
      case "machine": this.persistedMachines.set(payload.raw.id, payload.raw); await this.persistMachinesList(); break;
      case "session": this.persistedSessions.set(payload.raw.sessionId, payload.raw); await this.persistSessionsList(); break;
      case "events": await this.cache?.append(payload.sessionId, payload.raw); break;
      case "prefs": await this.keepValue("prefs", { bytes: toBinary(AccountPrefsObjectSchema, payload.raw) }); break;
      case "sessionOpen":
        if (payload.open) this.persistedOpen.add(payload.sessionId); else this.persistedOpen.delete(payload.sessionId);
        await this.keepValue("open-sessions", { ids: [...this.persistedOpen] });
        break;
    }
  }

  async deleteCache(eff: CacheDeleteEffect): Promise<void> {
    switch (eff.kind.case) {
      case "dropEventsSessionId": await this.cache?.drop(eff.kind.value); break;
      case "removeSessionId":
        await this.cache?.drop(eff.kind.value);
        this.persistedSessions.delete(eff.kind.value);
        this.persistedOpen.delete(eff.kind.value);
        this.scroll.delete(eff.kind.value);
        await this.persistSessionsList();
        await this.keepValue("open-sessions", { ids: [...this.persistedOpen] });
        break;
      case "unlistSessionId": this.persistedSessions.delete(eff.kind.value); await this.persistSessionsList(); break;
      case "removeMachineId": this.persistedMachines.delete(eff.kind.value); await this.persistMachinesList(); break;
    }
  }

  private persistMachinesList = () => this.keepValue("machines", { bytes: toBinary(ListMachinesResponseSchema, create(ListMachinesResponseSchema, { machines: [...this.persistedMachines.values()] })) });
  private persistSessionsList = () => this.keepValue("sessions", { bytes: toBinary(ListRemoteSessionsResponseSchema, create(ListRemoteSessionsResponseSchema, { sessions: [...this.persistedSessions.values()] })) });

  private rpcEv(event: CoreEvent["event"]): CoreEvent {
    return create(CoreEventSchema, { event });
  }

  async executeRpc(eff: RpcEffect): Promise<CoreEvent> {
    const opId = eff.opId;
    switch (eff.kind.case) {
      case "fetchLists":
        try {
          const [mRes, sRes, dRes] = await Promise.all([
            this.api.listMachines({}),
            this.api.listRemoteSessions({ limit: eff.kind.value.limit || this.sessionPageSize }),
            this.api.listDevices({}),
          ]);
          this.maybeDescribeSelf(dRes.devices);
          return this.rpcEv({ case: "listsFetched", value: create(ListsFetchedEventSchema, { opId, wireMachines: mRes.machines, wireSessions: sRes.sessions, next: sRes.next, wireDevices: dRes.devices }) });
        } catch (e) {
          return this.rpcEv({ case: "listsFetched", value: create(ListsFetchedEventSchema, { opId, error: errorText(e) }) });
        }
      case "fetchSessionsPage":
        try {
          const res = await this.api.listRemoteSessions({ after: eff.kind.value.after, limit: eff.kind.value.limit || this.sessionPageSize });
          return this.rpcEv({ case: "sessionsPageFetched", value: create(SessionsPageFetchedEventSchema, { opId, wireSessions: res.sessions, next: res.next }) });
        } catch (e) {
          return this.rpcEv({ case: "sessionsPageFetched", value: create(SessionsPageFetchedEventSchema, { opId, error: errorText(e) }) });
        }
      case "fetchSessionById": {
        const sid = eff.kind.value.sessionId;
        try {
          const res = await this.api.listRemoteSessions({ sessionIds: [sid], limit: 1 });
          const found = res.sessions.find((s) => s.sessionId === sid);
          return this.rpcEv({ case: "sessionByIdFetched", value: create(SessionByIdFetchedEventSchema, { opId, wireSession: found }) });
        } catch (e) {
          return this.rpcEv({ case: "sessionByIdFetched", value: create(SessionByIdFetchedEventSchema, { opId, error: errorText(e) }) });
        }
      }
      case "getEvents": {
        const { sessionId, afterSeq, limit } = eff.kind.value;
        try {
          const res = await this.api.getEvents({ sessionId, afterSeq, limit: limit || this.historyPageSize });
          return this.rpcEv({ case: "eventsPageFetched", value: create(EventsPageFetchedEventSchema, { opId, sessionId, wireEvents: res.events, more: res.more }) });
        } catch (e) {
          return this.rpcEv({ case: "eventsPageFetched", value: create(EventsPageFetchedEventSchema, { opId, sessionId, error: errorText(e) }) });
        }
      }
      case "getAccountPrefs":
        try {
          const res = await this.api.getAccountPrefs({ objectId: eff.kind.value.objectId || PREFS_OBJECT });
          return this.rpcEv({ case: "accountPrefsFetched", value: create(AccountPrefsFetchedEventSchema, { opId, wirePrefs: res.object }) });
        } catch (e) {
          return this.rpcEv({ case: "accountPrefsFetched", value: create(AccountPrefsFetchedEventSchema, { opId, error: errorText(e) }) });
        }
      case "putAccountPrefs": {
        const { objectId, expectedRevision, nextPrefs, sealedEnvelope } = eff.kind.value;
        try {
          const env = sealedEnvelope ?? (await this.sealSecureMessage({ kind: PayloadKind.ACCOUNT_PREFS, requestId: objectId || PREFS_OBJECT, seq: expectedRevision + 1n, sender: this.account.deviceId }, ProjectPrefsSchema, nextPrefs ?? emptyPrefs())).envelope;
          const res = await this.api.putAccountPrefs({ objectId: objectId || PREFS_OBJECT, expectedRevision, envelope: env });
          return this.rpcEv({ case: "accountPrefsPutResult", value: create(AccountPrefsPutResultEventSchema, { opId, applied: res.applied, wireCurrent: res.object }) });
        } catch (e) {
          const sealConflict = e instanceof SecurityError && (e.code === "clock" || e.code === "lease" || e.code === "conflict" || e.code === "stale");
          return this.rpcEv({ case: "accountPrefsPutResult", value: create(AccountPrefsPutResultEventSchema, { opId, sealConflict, error: errorText(e) }) });
        }
      }
      case "fetchDevices":
        try {
          const res = await this.api.listDevices({});
          this.maybeDescribeSelf(res.devices);
          return this.rpcEv({ case: "devicesFetched", value: create(DevicesFetchedEventSchema, { opId, wireDevices: res.devices }) });
        } catch (e) {
          return this.rpcEv({ case: "devicesFetched", value: create(DevicesFetchedEventSchema, { opId, error: errorText(e) }) });
        }
      default:
        throw new Error("unexpected rpc effect");
    }
  }

  private maybeDescribeSelf(devices: Device[]) {
    const me = devices.find((d) => d.id === this.account.deviceId);
    if (me && (!me.meta || !me.meta.securityHeader.length)) {
      void this.describeSelf();
    }
  }

  // --- CoreExecutor: Single-Attempt Command Dispatch (including Direct-First + Interactive Dual-Send) ---

  directSignalSend(eff: DirectSignalSendEffect) { this.directExec.signalSend(eff); }
  directPcCreate(eff: DirectPcCreateEffect) { this.directExec.pcCreate(eff); }
  directPcDescribe(eff: DirectPcDescribeEffect) { void this.directExec.pcDescribe(eff); }
  directPcSetRemote(eff: DirectPcSetRemoteEffect) { void this.directExec.pcSetRemote(eff); }
  directPcAddCandidates(eff: DirectPcAddCandidatesEffect) { void this.directExec.pcAddCandidates(eff); }
  directPcConfirm(eff: DirectPcConfirmEffect) { this.directExec.pcConfirm(eff); }
  directPcClose(eff: DirectPcCloseEffect) { this.directExec.pcClose(eff); }
  directDcReadStats(eff: DirectDcReadStatsEffect) { void this.directExec.dcReadStats(eff); }

  async executeCommandAttempt(eff: DispatchCommandEffect, signal?: AbortSignal): Promise<CommandOutcomeEvent> {
    const directRoute = eff.routeLeg === CommandRouteLeg.DIRECT && eff.directPcId > 0n
      ? { pcId: eff.directPcId, ticketId: eff.directTicketId, timeoutMs: eff.directTimeoutMs }
      : undefined;
    const { outcome } = await this.executeSealedAttempt({
      requestId: eff.requestId,
      attemptId: eff.attemptId,
      machineId: eff.machineId,
      sessionId: eff.sessionId,
      deadlineMs: Number(eff.deadlineMs),
      commandBytes: new Uint8Array(eff.commandBytes),
      sealedEnvelope: eff.sealedEnvelope,
      priorUnknown: eff.retryOfUnknown,
      directRoute,
      directTicketId: eff.directTicketId,
      signal,
    });
    return outcome;
  }

  private async executeSealedAttempt(params: {
    requestId: string;
    attemptId: string;
    machineId: string;
    sessionId: string;
    deadlineMs: number;
    commandBytes: Uint8Array<ArrayBuffer>;
    sealedEnvelope?: Envelope;
    priorUnknown: boolean;
    directRoute?: { pcId: bigint; ticketId: bigint; timeoutMs: bigint };
    directTicketId?: bigint;
    hasFiles?: boolean;
    signal?: AbortSignal;
    onHandoff?: () => () => void;
  }): Promise<{
    outcome: CommandOutcomeEvent;
    msg?: CommandResult;
    rawEnvelope?: Envelope;
    elapsedMs: number;
    observedBytes: bigint;
    ok: boolean;
  }> {
    const ticketId = params.directRoute?.ticketId ?? params.directTicketId ?? 0n;
    const isDirect = Boolean(params.directRoute && params.directRoute.pcId > 0n);
    const base = { requestId: params.requestId, attemptId: params.attemptId, directTicketId: ticketId };
    const mk = (fields: Omit<Partial<CommandOutcomeEvent>, "$typeName" | "requestId" | "attemptId" | "directTicketId">) =>
      create(CommandOutcomeEventSchema, { ...base, ...fields });
    if (this.closed) {
      return { outcome: mk({ source: OutcomeSource.LOCAL, notForwarded: true, transportCode: "canceled", routeWasDirect: isDirect }), elapsedMs: 0, observedBytes: 0n, ok: false };
    }
    if (this.serverAhead) {
      return {
        outcome: mk({
          source: OutcomeSource.LOCAL,
          notForwarded: !params.priorUnknown,
          transportCode: params.priorUnknown ? "unknown" : "clock",
          transportDetail: l10n.t("errors.this_device_s_clock_is_behind_the_account_state_remote"),
          routeWasDirect: isDirect,
        }),
        elapsedMs: 0,
        observedBytes: 0n,
        ok: false,
      };
    }

    let parsedCmd: ReturnType<typeof fromBinary<typeof CommandSchema>> | undefined;
    try { parsedCmd = fromBinary(CommandSchema, params.commandBytes); } catch { parsedCmd = undefined; }

    let envelope: Envelope;
    let requestKeyHash: Uint8Array<ArrayBuffer> | undefined;
    let payloadHash: Uint8Array<ArrayBuffer> | undefined;
    try {
      if (params.sealedEnvelope) {
        envelope = params.sealedEnvelope;
      } else {
        await this.checkNetworkSecurity();
        const sealed = await this.sealCommandBytes(params.machineId, params.sessionId, params.requestId, BigInt(params.deadlineMs), params.commandBytes);
        envelope = sealed.envelope;
        requestKeyHash = sealed.requestKeyHash;
        payloadHash = sealed.payloadHash;
      }
      if (params.hasFiles) {
        const issue = this.fileUploadIssue(params.machineId);
        if (issue) {
          return {
            outcome: mk({ source: OutcomeSource.LOCAL, notForwarded: true, transportCode: params.priorUnknown ? "unknown" : "failed_precondition", transportDetail: issue, routeWasDirect: isDirect }),
            elapsedMs: 0,
            observedBytes: 0n,
            ok: false,
          };
        }
      }
    } catch (e) {
      this.noteClock(e);
      if (params.priorUnknown) {
        return { outcome: mk({ source: OutcomeSource.LOCAL, notForwarded: false, handedOff: true, transportCode: "unknown", transportDetail: errorText(e), routeWasDirect: isDirect }), elapsedMs: 0, observedBytes: 0n, ok: false };
      }
      const code = e instanceof SecurityError && e.code === "clock" ? "clock" : e instanceof SecurityError && (e.code === "lease" || e.code === "stale") ? "failed_precondition" : "security_unavailable";
      return { outcome: mk({ source: OutcomeSource.LOCAL, notForwarded: true, transportCode: code, transportDetail: errorText(e), routeWasDirect: isDirect }), elapsedMs: 0, observedBytes: 0n, ok: false };
    }

    let handedOff = false;
    let undo: (() => void) | undefined;
    const handoff = () => {
      if (!handedOff) {
        handedOff = true;
        undo = params.onHandoff?.();
      }
    };
    const relayAttempt = isDirect ? undefined : this.deviceLink.path.attempt(handoff);
    const attemptId = isDirect ? params.attemptId : relayAttempt!.attemptId;
    const routeStarted = performance.now();

    try {
      params.signal?.throwIfAborted();
      let res;
      if (isDirect) {
        const env = await this.directExec.sendRpc(
          params.directRoute!.pcId,
          params.requestId,
          toBinary(EnvelopeSchema, envelope) as Uint8Array<ArrayBuffer>,
          params.directRoute!.timeoutMs,
          handoff,
          params.signal,
        );
        res = create(ForwardResponseSchema, { result: env, attemptId });
      } else {
        await relayAttempt!.prepare();
        res = await relayAttempt!.send(params.machineId, envelope, params.signal);
      }

      const elapsedMs = performance.now() - routeStarted;
      const attemptBound = res.attemptId === attemptId;
      if (params.sealedEnvelope) {
        if (
          parsedCmd?.kind.case === "schedule" &&
          parsedCmd.kind.value.operation === "list" &&
          res.result &&
          this.isMachineAuthorized(params.machineId)
        ) {
          try {
            const { msg: schedMsg } = await this.openSecureMessage(
              res.result,
              { kind: PayloadKind.COMMAND_RESULT, machineId: params.machineId, sessionId: params.sessionId, requestId: params.requestId },
              CommandResultSchema,
            );
            if (!schedMsg.errorCode && schedMsg.outcome === "executed" && schedMsg.kind.case === "schedule" && schedMsg.kind.value.resultJson.length) {
              await this.keepValue("schedules:" + params.machineId, { bytes: toBinary(EnvelopeSchema, res.result), requestId: params.requestId });
            }
          } catch {
            // Core will reject/report any unverified envelope.
          }
        }
        return {
          rawEnvelope: res.result,
          elapsedMs,
          observedBytes: BigInt(parsedCmd?.kind.case === "putImageChunk" ? parsedCmd.kind.value.data?.length ?? 0 : 0),
          ok: attemptBound && (isDirect || res.outcome === "executed"),
          outcome: mk({
            source: OutcomeSource.DAEMON,
            attemptBound,
            outerOutcome: isDirect ? "" : res.outcome,
            outerReason: isDirect ? "" : res.reason,
            sealedResult: res.result,
            handedOff,
            routeWasDirect: isDirect,
            elapsedMs,
          }),
        };
      }

      let msg: CommandResult;
      try {
        msg = (await this.openSecureMessage(res.result, { kind: PayloadKind.COMMAND_RESULT, machineId: params.machineId, sessionId: params.sessionId, requestId: params.requestId }, CommandResultSchema)).msg;
      } catch {
        return {
          outcome: mk({
            source: OutcomeSource.DAEMON,
            attemptBound,
            signed: false,
            hashesMatch: false,
            handedOff,
            routeWasDirect: isDirect,
            elapsedMs,
          }),
          elapsedMs,
          observedBytes: 0n,
          ok: false,
        };
      }
      const hashesMatch = !!requestKeyHash && !!payloadHash && equalBytes(msg.requestKeyHash, requestKeyHash) && equalBytes(msg.payloadHash, payloadHash) && (msg.outcome === "conflict" || msg.conflictKind === "");
      const outerMatchesInner = isDirect || (res.outcome === msg.outcome && res.reason === msg.reason);
      const bytes = BigInt(
        parsedCmd?.kind.case === "putImageChunk"
          ? parsedCmd.kind.value.data?.length ?? 0
          : msg.kind.case === "getSharedFileChunk"
            ? msg.kind.value.data.length
            : 0,
      );
      const ok = hashesMatch && attemptBound && outerMatchesInner && !msg.errorCode && msg.outcome === "executed";
      if (
        parsedCmd?.kind.case === "schedule" &&
        parsedCmd.kind.value.operation === "list" &&
        ok &&
        res.result &&
        this.isMachineAuthorized(params.machineId)
      ) {
        await this.keepValue("schedules:" + params.machineId, { bytes: toBinary(EnvelopeSchema, res.result), requestId: params.requestId });
      }
      return {
        msg,
        rawEnvelope: res.result,
        elapsedMs,
        observedBytes: bytes,
        ok,
        outcome: mk({
          source: OutcomeSource.DAEMON,
          signed: true,
          hashesMatch,
          attemptBound,
          outerMatchesInner,
          outerOutcome: isDirect ? msg.outcome : res.outcome,
          outerReason: isDirect ? msg.reason : res.reason,
          result: msg,
          handedOff,
          routeWasDirect: isDirect,
          elapsedMs,
          observedBytes: bytes,
        }),
      };
    } catch (e) {
      const elapsedMs = performance.now() - routeStarted;
      if (e instanceof PathChanged) {
        return {
          outcome: mk({ source: OutcomeSource.TRANSPORT, notForwarded: !handedOff, handedOff, transportCode: "path_changed", routeWasDirect: isDirect, elapsedMs }),
          elapsedMs,
          observedBytes: 0n,
          ok: false,
        };
      }
      if (!isDirect && e instanceof ConnectError && relayAttempt) {
        const refusal = relayAttempt.refusal(e);
        if (refusal !== undefined) {
          undo?.();
          undo = undefined;
          return {
            outcome: mk({
              source: OutcomeSource.SERVER,
              notForwarded: true,
              attemptBound: true,
              outerReason: refusal.reason ?? "",
              transportCode: connectErrorCode(e.code),
              transportDetail: refusal.reason === "machine_offline" ? "" : (e.rawMessage || e.message),
              routeWasDirect: false,
              elapsedMs,
            }),
            elapsedMs,
            observedBytes: 0n,
            ok: false,
          };
        }
        if (!handedOff && e.code === Code.Unauthenticated) {
          return {
            outcome: mk({ source: OutcomeSource.SERVER, notForwarded: true, attemptBound: true, transportCode: "unauthenticated", transportDetail: e.rawMessage || e.message, routeWasDirect: false, elapsedMs }),
            elapsedMs,
            observedBytes: 0n,
            ok: false,
          };
        }
        return {
          outcome: mk({ source: OutcomeSource.SERVER, notForwarded: !handedOff, handedOff, attemptBound: false, transportCode: connectErrorCode(e.code), transportDetail: e.rawMessage || e.message, routeWasDirect: false, elapsedMs }),
          elapsedMs,
          observedBytes: 0n,
          ok: false,
        };
      }
      return {
        outcome: mk({ source: OutcomeSource.TRANSPORT, notForwarded: !handedOff, handedOff, transportCode: !this.network ? "network" : "unknown", transportDetail: errorText(e), routeWasDirect: isDirect, elapsedMs }),
        elapsedMs,
        observedBytes: 0n,
        ok: false,
      };
    }
  }

  // --- Security Helpers (backed by clientcore/security) ---

  private async openSecureMessage<D extends DescMessage>(
    env: Envelope | undefined,
    want: { kind: PayloadKind; machineId?: string; sessionId?: string; requestId?: string; seq?: bigint },
    schema: D,
    history = false,
  ) {
    if (!env) throw new Error(l10n.t("errors.security_envelope_missing"));
    if ([PayloadKind.ACCOUNT_PREFS, PayloadKind.DEVICE_META, PayloadKind.MACHINE_META, PayloadKind.SESSION_META, PayloadKind.IMAGE].includes(want.kind)) history = true;
    if (!history) await this.ensureWakeSecurity();
    const res = await this.ensureHost().securityOp({
      case: "openEnvelope",
      value: create(SecurityOpenEnvelopeOpSchema, {
        envelope: env,
        expectedPurpose: purposeOf(want.kind),
        machineId: want.machineId ?? "",
        sessionId: want.sessionId ?? "",
        requestId: want.requestId ?? "",
        seq: want.seq ?? 0n,
        history,
      }),
    });
    if (res.result.case !== "opened") throw new SecurityError();
    const opened = res.result.value;
    try {
      const msg = fromBinary(schema, opened.plaintext);
      return {
        header: {
          version: 3,
          kind: want.kind,
          machineId: opened.machineId,
          sessionId: opened.sessionId,
          requestId: opened.requestId,
          seq: opened.seq,
          deadlineMs: opened.deadlineMs,
          sender: opened.sender,
        },
        contentEpoch: opened.contentEpoch,
        msg,
        size: opened.plaintext.length,
      };
    } catch {
      throw new Error(l10n.t("errors.security_body_type_or_direction_mismatch"));
    }
  }

  private async checkNetworkSecurity() {
    if (this.verifiedNetworkGeneration === this.networkGeneration) return;
    const generation = this.networkGeneration, wake = this.wakeRevision;
    await this.ensureHost().securityClient.syncNow(false, this.token);
    this.confirmWake(generation, wake);
    if (generation !== this.networkGeneration) throw new SecurityError("stale");
    this.verifiedNetworkGeneration = generation;
  }

  private sealCommandBytes(machineId: string, sessionId: string, requestId: string, deadlineMs: bigint, commandBytes: Uint8Array<ArrayBuffer>) {
    return this.sealSecureMessage({ kind: PayloadKind.COMMAND, machineId, sessionId, requestId, seq: 0n, deadlineMs, sender: this.account.deviceId }, CommandSchema, create(CommandSchema), undefined, commandBytes);
  }

  private async sealSecureMessage<D extends DescMessage>(
    h: Omit<Partial<Header>, "$typeName">,
    schema: D, msg: MessageShape<D>, beforeSign?: () => void, preparedBytes?: Uint8Array<ArrayBuffer>,
  ): Promise<{ envelope: Envelope; requestKeyHash: Uint8Array<ArrayBuffer>; payloadHash: Uint8Array<ArrayBuffer> }> {
    await this.ensureWakeSecurity();
    if (h.kind === PayloadKind.COMMAND) await this.checkNetworkSecurity();
    let purpose = purposeOf(h.kind ?? PayloadKind.UNSPECIFIED);
    if (h.kind === PayloadKind.TERMINAL_INPUT) {
      const f = msg as unknown as { kind: { case?: string } };
      if (f.kind.case === "resize") purpose = Purpose.TerminalResize;
      else if (f.kind.case === "historyRequest") purpose = Purpose.TerminalHistory;
    }
    const useSessionSeed = purpose === Purpose.TerminalInput || purpose === Purpose.TerminalResize || purpose === Purpose.TerminalHistory;
    const plaintext = preparedBytes ?? (toBinary(schema, msg) as Uint8Array<ArrayBuffer>);
    beforeSign?.();
    const res = await this.ensureHost().securityOp({
      case: "sealEnvelope",
      value: create(SecuritySealEnvelopeOpSchema, {
        purpose,
        machineId: h.machineId ?? "",
        sessionId: h.sessionId ?? "",
        requestId: h.requestId ?? "",
        seq: h.seq ?? 0n,
        deadlineMs: h.deadlineMs ?? (h.kind === PayloadKind.TERMINAL_INPUT ? BigInt(Date.now() + 60000) : 0n),
        plaintext,
        useSessionSeed,
      }),
    });
    if (res.result.case !== "sealed" || !res.result.value.envelope) throw new SecurityError();
    return {
      envelope: res.result.value.envelope,
      requestKeyHash: new Uint8Array(res.result.value.requestKeyHash),
      payloadHash: new Uint8Array(res.result.value.payloadHash),
    };
  }

  private roundBudget(): number { return this.everOnline || this.networkGeneration > 0 ? 3000 : SETUP_IDLE_MS; }

  private retireNetworkIO() {
    this.loginController?.abort(new PathChanged());
    this.wakeController?.abort(new PathChanged());
    this.wakeSecurity = undefined;
    this.wakeRequired = true;
    this.wakeRevision++;
  }
  private confirmWake(generation: number, wake: number) { if (generation === this.networkGeneration && wake === this.wakeRevision) this.wakeRequired = false; }

  private async ensureWakeSecurity() {
    if (!this.wakeRequired) return;
    if (!this.wakeSecurity) {
      const generation = this.networkGeneration, wake = this.wakeRevision, controller = new AbortController();
      this.wakeController = controller;
      const pending = ownedRound(controller, this.roundBudget(), async () => {
        await this.ensureHost().securityClient.syncNow(false, this.token);
        this.confirmWake(generation, wake);
        this.verifiedNetworkGeneration = generation;
      }).finally(() => { if (this.wakeSecurity === pending) { this.wakeSecurity = undefined; this.wakeController = undefined; } });
      this.wakeSecurity = pending;
      void pending.catch(() => undefined);
    }
    return this.wakeSecurity;
  }

  private async login(waiter?: AbortSignal): Promise<void> {
    while (true) {
      waiter?.throwIfAborted();
      let job = this.loggingIn, controller = this.loginController;
      if (!job || !controller) {
        controller = new AbortController();
        const owner = controller;
        const pending = ownedRound(owner, this.roundBudget(), (signal) => this.signIn(signal)).finally(() => {
          if (this.loggingIn === pending) { this.loggingIn = undefined; this.loginController = undefined; }
        });
        job = pending;
        this.loginController = owner;
        this.loggingIn = pending;
        void pending.catch(() => undefined);
      }
      try { return await abortable(job, waiter); }
      catch (error) { if (controller.signal.reason instanceof PathChanged && !this.closed && !waiter?.aborted) continue; throw error; }
    }
  }

  private async signIn(signal: AbortSignal) {
    const auth = createClient(AuthService, createConnectTransport({ fetch: relayFetch, baseUrl: apiBase(this.account.server), useBinaryFormat: true, defaultTimeoutMs: OPEN_TIMEOUT_MS }));
    for (let again = true; ; again = false) {
      try {
        const timeoutMs = this.everOnline || this.networkGeneration > 0 ? 3000 : OPEN_TIMEOUT_MS;
        const ch = await auth.getChallenge({ deviceId: this.account.deviceId }, { signal, timeoutMs });
        signal.throwIfAborted();
        const challengeMsg = concat(utf8("remote-agent/v1/login\0"), new Uint8Array(ch.challenge));
        const signature = await this.secStore.signKey(SecurityKeyKind.DEVICE_ED, "", challengeMsg);
        signal.throwIfAborted();
        const res = await auth.login({ protocolVersion: RELAY_PROTOCOL_VERSION, deviceId: this.account.deviceId, challenge: ch.challenge, signature }, { signal, timeoutMs });
        signal.throwIfAborted();
        if (this.closed) return;
        this.token = res.token;
        this.tokenExpires = Number(res.expiresMs);
        this.tokenWrites = this.tokenWrites.then(() => saveLoginToken(this.account.server, this.account.deviceId, { token: res.token, expiresMs: Number(res.expiresMs) })).catch(() => undefined);
        await this.tokenWrites;
        return;
      } catch (e) {
        signal.throwIfAborted();
        if (!(e instanceof ConnectError) || e.code !== Code.PermissionDenied) throw e;
        if (again) continue;
        this.discardToken();
        await this.tokenWrites;
        throw e;
      }
    }
  }

  private noteClock(e: unknown) { if (e instanceof SecurityError && e.code === "clock") this.clockWarning = l10n.t("errors.this_device_s_clock_is_behind_the_account_state"); }

  private keepValue(name: string, value: Record<string, unknown> | undefined) {
    return this.cache?.set(name, value && { ...value, deviceId: this.account.deviceId, server: this.account.server }).catch(() => undefined);
  }

  // --- Transitional Step 3/4 Command Runner (reuses executeSealedAttempt) ---

  protected relayEpoch(): Promise<string> { return relayEpoch(apiBase(this.account.server)); }

  fileUploadIssue(machineId: string): string { return this.peer(machineId).capabilities?.fileUpload === true ? "" : l10n.t("attachment.update_required"); }

  private checkFileCapability(machineId: string, cmd: CommandKind, mayHaveRun = false) {
    if ((cmd?.case !== "send" && cmd?.case !== "createSession") || !cmd.value.fileIds?.length) return;
    const issue = this.fileUploadIssue(machineId);
    if (issue) throw new CommandError(mayHaveRun ? "unknown_outcome" : "failed_precondition", issue, false, false, mayHaveRun ? "unknown" : "not_sent");
  }

  private readonly authorizedCommands = new WeakMap<RequestSlot, { machineId: string; sid: string; requestId: string; deadline: number; cmd: CommandKind; plain: Uint8Array<ArrayBuffer> }>();

  private preparePayload(machineId: string, sid: string, cmd: CommandKind, slot: RequestSlot) {
    const previous = this.authorizedCommands.get(slot);
    if (previous) return previous;
    const saved = slot.prepared;
    const plain = saved ? new Uint8Array(saved.plain) : (toBinary(CommandSchema, create(CommandSchema, { kind: cmd, protocolVersion: PEER_PROTOCOL_VERSION })) as Uint8Array<ArrayBuffer>);
    const prepared = { requestId: slot.requestId, deadline: slot.deadline, machineId: saved?.machineId ?? machineId, sid: saved?.sid ?? sid, plain: new Uint8Array(plain) as Uint8Array<ArrayBuffer>, cmd: fromBinary(CommandSchema, plain).kind };
    this.authorizedCommands.set(slot, prepared);
    slot.prepared = { machineId: prepared.machineId, sid: prepared.sid, plain: new Uint8Array(plain) };
    return prepared;
  }

  async command(sid: string, cmd: CommandKind, slot = newSlot()): Promise<CommandResult> {
    const authorized = this.authorizedCommands.get(slot) ?? (slot.prepared && this.preparePayload("", "", cmd, slot));
    if (authorized) return this.forwardCall(authorized.machineId, authorized.sid, authorized.cmd, slot);
    const s = this.syncSession(sid);
    if (!s) throw new Error(l10n.t("session.android_unknown"));
    return this.forwardCall(s.machineId, sid, cmd, slot);
  }

  machineCommand(machineId: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> { return this.forward(machineId, "", cmd, slot); }
  private forwardCall(machineId: string, sid: string, cmd: CommandKind, slot: RequestSlot): Promise<CommandResult> { return this.forward(machineId, sid, cmd, slot); }

  private async forward(machineId: string, sid: string, cmd: CommandKind, slot = newSlot(), signal?: AbortSignal): Promise<CommandResult> {
    if (this.closed) throw new CommandError("canceled", l10n.t("errors.terminal_connection_canceled"), false, false, "not_sent");
    const prepared = this.preparePayload(machineId, sid, cmd, slot);
    machineId = prepared.machineId; sid = prepared.sid; cmd = prepared.cmd;
    const { requestId, deadline, plain } = prepared;
    if (!slot.evidence.unresolved && Date.now() >= deadline + COMMAND_EXPIRY_SKEW_MS) throw new CommandError("expired", l10n.t("errors.connection_timed_out"));
    const sm = this.syncMachine(machineId);
    const theirs = this.peer(machineId).protocolVersion;
    if (theirs !== undefined && theirs !== PEER_PROTOCOL_VERSION) {
      throw new CommandError("failed_precondition", theirs < PEER_PROTOCOL_VERSION ? l10n.t("errors.remote_agent_on_is_out_of_date_update_it", { value: String(machineName(sm ? { id: sm.machineId, meta: sm.meta } : undefined)) }) : l10n.t("errors.this_page_is_out_of_date_refresh_or_update"), false, false, slot.evidence.unresolved ? "unknown" : "not_sent");
    }
    this.checkFileCapability(machineId, cmd, slot.evidence.unresolved);
    await slot.beforeDispatch?.(slot);

    const routeParams = (() => {
      if (cmd?.case === "getSharedFileChunk") {
        return { routeHint: CommandRouteHint.BULK, bulkFileKey: `${cmd.value.sessionId}/${cmd.value.fileId}`, bulkBytes: 0n, bulkUpload: false };
      }
      if (cmd?.case === "putImageChunk") {
        return { routeHint: CommandRouteHint.BULK, bulkFileKey: cmd.value.transferId, bulkBytes: BigInt(cmd.value.data?.length ?? 0), bulkUpload: true };
      }
      if (readOnlyCommand(cmd) && cmd?.case !== "getImageUploadStatus") {
        return { routeHint: CommandRouteHint.READ };
      }
      return {};
    })();
    const uploadTransferId = cmd?.case === "beginImageUpload" ? cmd.value.transferId : "";
    const uploadHasFilename = cmd?.case === "beginImageUpload" && !!cmd.value.fileName;
    const finishUploadTransferId =
      cmd?.case === "completeImageUpload" || cmd?.case === "abortImageUpload" ? cmd.value.transferId : "";

    let forceRelay = false, attempted = false, recoveryUntil: number | undefined;
    for (let attempt = 0; ; attempt++) {
      signal?.throwIfAborted();
      if (attempted && Date.now() >= deadline + (slot.evidence.unresolved ? RESULT_RETENTION_MS : 0)) {
        throw new CommandError("unknown_outcome", l10n.t("errors.connection_timed_out"), false, false, "unknown");
      }
      this.checkFileCapability(machineId, cmd, slot.evidence.unresolved);
      attempted = true;
      const priorUnknown = !!slot.evidence.unresolved;
      const priorOutcome: Outcome = priorUnknown ? "unknown" : "not_sent";
      const dec = routeParams.routeHint || uploadTransferId
        ? await (this.hostBridge?.beginDirectBulkTransfer({
            machineId,
            ...routeParams,
            deadlineMs: BigInt(deadline),
            forceRelay,
            uploadTransferId,
            uploadHasFilename,
          }) ?? Promise.resolve({ useDirect: false, pcId: 0n, ticketId: 0n, timeoutMs: 0n }))
        : { useDirect: false, pcId: 0n, ticketId: 0n, timeoutMs: 0n };
      const directRoute = dec.useDirect && !forceRelay && dec.pcId > 0n
        ? { pcId: dec.pcId, ticketId: dec.ticketId, timeoutMs: dec.timeoutMs }
        : undefined;
      const { outcome: ev, msg, elapsedMs, observedBytes, ok } = await this.executeSealedAttempt({
        requestId, attemptId: newRequestID(), machineId, sessionId: sid, deadlineMs: deadline, commandBytes: plain,
        priorUnknown, directRoute, directTicketId: dec.ticketId,
        hasFiles: (cmd?.case === "send" || cmd?.case === "createSession") && !!cmd.value.fileIds?.length,
        signal, onHandoff: () => slot.evidence.trackHandoff(),
      });
      this.hostBridge?.finishDirectBulkTransfer({
        ticketId: dec.ticketId,
        routeWasDirect: Boolean(directRoute),
        ok,
        elapsedMs,
        observedBytes,
        finishUploadTransferId,
        machineId,
      });

      if (ev.source === OutcomeSource.DAEMON) {
        const category = (ev.outerOutcome || msg?.outcome || "unknown") as Outcome;
        if (!ev.signed || !ev.hashesMatch || !ev.attemptBound || !ev.outerMatchesInner || !msg || category === "unknown") {
          slot.evidence.includePrior(true);
          throw new CommandError("unknown_outcome", l10n.t("errors.the_computer_s_reply_could_not_be_verified_command"), true, false, "unknown");
        }
        if ((category === "executed" && !msg.resultAvailable) || (category === "conflict" && msg.conflictKind === "identity")) {
          this.retryList();
          throw new CommandError(msg.errorCode, l10n.t(category === "executed" ? "errors.executed_result_unavailable" : "errors.request_identity_conflict"), true, false, category, msg.conflictKind, msg.resultAvailable);
        }
        if (category !== "executed" || msg.errorCode) throw new CommandError(msg.errorCode, msg.errorMessage, true, readOnlyCommand(cmd), category, msg.conflictKind, msg.resultAvailable);
        return msg;
      }

      if (!ev.notForwarded) slot.evidence.includePrior(true);
      if (ev.routeWasDirect && routeParams.routeHint) {
        forceRelay = true;
        if (cmd?.case === "putImageChunk") await this.machineCommand(machineId, { case: "getImageUploadStatus", value: { transferId: cmd.value.transferId } });
        if (ev.transportCode === "path_changed" && Date.now() < deadline) { attempt--; continue; }
      }
      if (ev.transportCode === "path_changed") { attempt--; continue; }
      if (ev.source === OutcomeSource.LOCAL) {
        const liveOutcome: Outcome = slot.evidence.unresolved ? "unknown" : priorOutcome;
        throw new CommandError(ev.transportCode === "unknown" || liveOutcome === "unknown" ? "unknown_outcome" : ev.transportCode, ev.transportDetail, false, false, liveOutcome);
      }
      if (ev.notForwarded && ev.attemptBound && ev.transportCode === "unauthenticated" && attempt < 3) { await this.login(signal); continue; }
      if (ev.notForwarded && ev.attemptBound) {
        throw new CommandError(ev.transportCode, ev.outerReason === "machine_offline" ? l10n.t("errors.the_computer_is_offline_the_command_was_not_sent") : ev.transportDetail, false, false, priorOutcome);
      }
      if (ev.transportCode === "unavailable" && slot.evidence.unresolved && this.started && !this.closed) {
        signal?.throwIfAborted();
        if (recoveryUntil === undefined) { recoveryUntil = performance.now() + 3000; if (!ev.notForwarded) this.reconnectNow(); }
        if (performance.now() + 250 < recoveryUntil) { await abortable(new Promise<void>((resolve) => setTimeout(resolve, 250)), signal); continue; }
      }
      const delay = 1000 * (attempt + 1);
      if (["deadline_exceeded", "unknown", "internal", "canceled", "aborted"].includes(ev.transportCode) && attempt < 3 && Date.now() + delay < deadline) {
        await new Promise((r) => setTimeout(r, delay)); continue;
      }
      if (ev.transportCode === "unavailable") {
        throw slot.evidence.unresolved
          ? new CommandError("unknown_outcome", l10n.t("errors.connection_dropped_midway"), false, false, "unknown")
          : new CommandError("unavailable", l10n.t("errors.the_computer_is_offline_the_command_was_not_sent"));
      }
      throw new CommandError("unknown_outcome", ev.transportDetail || l10n.t("errors.the_relay_did_not_provide_a_verifiable_execution_result"), false, false, "unknown");
    }
  }

  // --- Schedules / Drafts / Outbox / Answers / Images / Terminal / Models / Settings / Devices ---

  async readScheduleCache(opId: bigint, machineId: string): Promise<ScheduleCacheLoadedEvent> {
    if (!machineId || this.closed || !this.isMachineAuthorized(machineId)) {
      return create(ScheduleCacheLoadedEventSchema, { opId, machineId });
    }
    try {
      const kept = (await this.cache?.get("schedules:" + machineId)) as { requestId?: unknown; bytes?: unknown } | undefined;
      if (!kept || typeof kept.requestId !== "string" || !(kept.bytes instanceof Uint8Array)) {
        return create(ScheduleCacheLoadedEventSchema, { opId, machineId });
      }
      const { msg } = await this.openSecureMessage(
        fromBinary(EnvelopeSchema, kept.bytes),
        { kind: PayloadKind.COMMAND_RESULT, machineId, sessionId: "", requestId: kept.requestId },
        CommandResultSchema,
        true,
      );
      if (msg.kind.case !== "schedule" || !msg.kind.value.resultJson.length) {
        return create(ScheduleCacheLoadedEventSchema, { opId, machineId });
      }
      return create(ScheduleCacheLoadedEventSchema, { opId, machineId, resultJson: msg.kind.value.resultJson });
    } catch (e) {
      return create(ScheduleCacheLoadedEventSchema, { opId, machineId, error: errorText(e) });
    }
  }

  async deleteScheduleCache(machineId: string): Promise<void> {
    if (!machineId) return;
    await this.keepValue("schedules:" + machineId, undefined);
  }

  private scheduleSeenStorageKey(machineId: string): string {
    return "ra-schedule-seen:" + JSON.stringify([this.account.server, this.account.deviceId, machineId]);
  }

  async loadScheduleSeen(machineId: string): Promise<ScheduleSeenLoadedEvent> {
    if (!machineId) return create(ScheduleSeenLoadedEventSchema, { machineId, found: false });
    try {
      const raw = typeof localStorage !== "undefined" ? localStorage.getItem(this.scheduleSeenStorageKey(machineId)) : null;
      if (raw === null) return create(ScheduleSeenLoadedEventSchema, { machineId, found: false });
      const arr = JSON.parse(raw) as unknown;
      if (!Array.isArray(arr)) return create(ScheduleSeenLoadedEventSchema, { machineId, found: false });
      const tokens: ScheduleSeenToken[] = [];
      for (const entry of arr) {
        if (typeof entry !== "string") continue;
        const pair = JSON.parse(entry) as unknown;
        if (Array.isArray(pair) && typeof pair[0] === "string" && typeof pair[1] === "string") {
          tokens.push(create(ScheduleSeenTokenSchema, { taskId: pair[0], runId: pair[1] }));
        }
      }
      return create(ScheduleSeenLoadedEventSchema, { machineId, tokens, found: true });
    } catch (e) {
      return create(ScheduleSeenLoadedEventSchema, { machineId, found: false, error: errorText(e) });
    }
  }

  async saveScheduleSeen(machineId: string, tokens: ScheduleSeenToken[]): Promise<void> {
    if (!machineId || typeof localStorage === "undefined") return;
    try {
      const encoded = tokens.map((t) => JSON.stringify([t.taskId, t.runId]));
      localStorage.setItem(this.scheduleSeenStorageKey(machineId), JSON.stringify(encoded));
    } catch {
      /* Keep working when storage is unavailable. */
    }
  }

  dismissScheduleNotifications(runs: ScheduleNotificationRef[]) {
    for (const run of runs) void closeScheduleNotification(run);
  }

  clearNotice(sid: string) {
    if (this.notices.delete(sid)) this.changed();
  }

  async openTerminal(
    sid: string,
    onFrame: (f: TerminalFrame) => void | Promise<void>,
    onClose: (reason: string) => void,
    signal?: AbortSignal,
  ): Promise<TermConn> {
    if (this.closed) throw new Error(l10n.t("errors.terminal_connection_closed"));
    const s = this.sessions.get(sid);
    if (!s) throw new Error(l10n.t("session.android_unknown"));
    if (signal?.aborted) throw new Error(l10n.t("errors.terminal_connection_canceled"));
    const streamId = newRequestID();
    const ws = this.deviceLink.openPathStream(apiBase(this.account.server).replace(/^http/, "ws") + "/v1/term");
    let closed = false;
    let openingTimer: ReturnType<typeof setTimeout> | undefined;
    let rejectOpening: (e: Error) => void = () => {};
    const abort = () => finish(l10n.t("errors.terminal_connection_canceled"));
    const stream = this.deviceLink.openStream(onClose, (reason) => {
      closed = true;
      clearTimeout(openingTimer);
      signal?.removeEventListener("abort", abort);
      rejectOpening(new Error(reason));
      ws.close();
    });
    const finish = (reason: string, notify = true) => stream.end(reason, notify);
    signal?.addEventListener("abort", abort, { once: true });
    await new Promise<void>((resolve, reject) => {
      rejectOpening = reject;
      openingTimer = setTimeout(() => finish(l10n.t("errors.terminal_connection_timed_out_reconnect")), OPEN_TIMEOUT_MS);
      ws.onOpen = () => {
        if (closed) return;
        clearTimeout(openingTimer);
        try {
          ws.send(toBinary(TermHelloSchema, create(TermHelloSchema, { token: this.token, streamId, machineId: s.machineId })));
          resolve();
        } catch (e) {
          finish(errorText(e));
        }
      };
      ws.onError = () => finish(l10n.t("errors.cannot_connect_to_terminal"));
      ws.onEnd = () => finish(l10n.t("errors.terminal_connection_closed"));
    });
    let last = 0n;
    let queue = Promise.resolve();
    let pendingBytes = 0;
    let pendingFrames = 0;
    ws.onFrame = (data) => {
      if (closed) return;
      const size = data.byteLength;
      pendingBytes += size;
      pendingFrames++;
      if (pendingBytes > 2 * 1024 * 1024 || pendingFrames > 512) {
        finish(l10n.t("errors.terminal_output_backlog_reconnect_for_the_latest_display"));
        ws.close();
        return;
      }
      queue = queue.then(async () => {
        try {
          if (closed) return;
          const env = fromBinary(EnvelopeSchema, new Uint8Array(data));
          const { header, msg } = await this.openSecureMessage(
            env,
            { kind: PayloadKind.TERMINAL_OUTPUT, machineId: s.machineId, sessionId: sid, requestId: streamId },
            TerminalFrameSchema,
          );
          if (closed) return;
          if (header.seq <= last) throw new Error(l10n.t("errors.terminal_frames_are_out_of_order"));
          last = header.seq;
          await onFrame(msg);
        } catch (e) {
          finish(errorText(e));
          ws.close();
        } finally {
          pendingBytes -= size;
          pendingFrames--;
        }
      });
    };
    ws.onEnd = () => finish(l10n.t("errors.terminal_connection_dropped"));
    if (closed) throw new Error(l10n.t("errors.terminal_connection_closed"));
    try {
      await this.command(sid, { case: "openTerminal", value: { sessionId: sid, streamId } });
    } catch (e) {
      finish(errorText(e));
      throw e;
    }
    if (closed) throw new Error(l10n.t("errors.terminal_connection_closed"));
    let seq = 0n;
    let sending = Promise.resolve();
    return {
      send: (frame) => {
        sending = sending.then(async () => {
          if (closed || !ws.ready) return;
          seq++;
          const env = (await this.sealSecureMessage(
            { kind: PayloadKind.TERMINAL_INPUT, machineId: s.machineId, sessionId: sid, requestId: streamId, seq, sender: this.account.deviceId },
            TerminalFrameSchema,
            create(TerminalFrameSchema, frame),
          )).envelope;
          if (!closed && ws.ready) ws.send(toBinary(EnvelopeSchema, env));
        });
      },
      close: () => finish(l10n.t("errors.terminal_connection_closed"), false),
    };
  }

  async getImage(sid: string, imageId: string): Promise<ImageResult> {
    const key = sid + "/" + imageId;
    const hit = this.imageCache.get(key);
    if (hit) { this.imageCache.delete(key); this.imageCache.set(key, hit); return hit; }
    const s = this.sessions.get(sid);
    if (!s) throw new Error(l10n.t("session.android_unknown"));
    let res;
    try {
      res = await this.api.getImage({ sessionId: sid, imageId });
    } catch (e) {
      if (e instanceof ConnectError && e.code === Code.NotFound) return { kind: "missing" };
      throw e;
    }
    if (res.expired || !res.image) return { kind: "expired" };
    let out: ImageResult;
    try {
      const { msg } = await this.openSecureMessage(
        res.image,
        { kind: PayloadKind.IMAGE, machineId: s.machineId, sessionId: sid, requestId: imageId },
        ImagePayloadSchema,
        true,
      );
      out = matchesFormat(msg.data, msg.format)
        ? { kind: "ok", blob: new Blob([msg.data as Uint8Array<ArrayBuffer>], { type: mimeOf(msg.format) }), width: msg.width, height: msg.height, format: msg.format }
        : { kind: "bad" };
    } catch (e) {
      out = { kind: "bad", ...(e instanceof UnsupportedHistoryError ? { why: e.message } : {}) };
    }
    if (out.kind === "ok") {
      this.imageCache.set(key, out);
      while (this.imageCache.size > IMAGE_CACHE) this.imageCache.delete(this.imageCache.keys().next().value!);
    }
    return out;
  }

  cachedImage(sid: string, imageId: string): ImageResult | undefined { return this.imageCache.get(sid + "/" + imageId); }
  authHeader(): string { return "Bearer " + this.token; }
  async pushKey(): Promise<string> { return (await this.api.getPushKey({})).publicKey; }
  async setPushSubscription(subscriptionJson: string, kinds: NotifyKind[]): Promise<void> {
    const capability = subscriptionJson ? await this.api.getPushKey({}) : undefined;
    const selected = subscriptionJson ? machineNotificationKinds(kinds, capability?.machineDiskNotifications === true) : [];
    await this.api.setPushSubscription({ subscriptionJson, kinds: selected });
  }

  listModels(machineId: string, agent: Agent = DEFAULT_AGENT, fresh = false): Promise<ListModelsResponse> {
    const key = machineId + "/" + agent;
    let p = fresh && !this.modelReads.has(key) ? undefined : this.models.get(key);
    if (!p) {
      p = this.machineCommand(machineId, { case: "listModels", value: { agent } }).then((r) => {
        if (r.kind.case !== "listModels") throw new Error(l10n.t("new_session.unexpected_reply"));
        this.changed();
        return r.kind.value;
      });
      this.modelReads.add(key);
      p.then(
        () => this.modelReads.delete(key),
        () => { this.modelReads.delete(key); if (this.models.get(key) === p) this.models.delete(key); },
      );
      this.models.set(key, p);
    }
    return p;
  }

  settings(machineId: string, fresh = false): Promise<DaemonSettings | undefined> {
    let p = fresh ? undefined : this.settingsCache.get(machineId);
    if (!p) {
      p = this.machineCommand(machineId, { case: "getSettings", value: {} }).then((r) => {
        if (r.kind.case !== "getSettings") throw new Error(l10n.t("new_session.unexpected_reply"));
        if (this.settingsCache.get(machineId) === p) this.changed();
        return r.kind.value.settings;
      });
      p.catch(() => { if (this.settingsCache.get(machineId) === p) this.settingsCache.delete(machineId); });
      this.settingsCache.set(machineId, p);
    }
    return p;
  }

  async pingServer(timeoutMs = 8000): Promise<number> {
    const stop = new AbortController();
    const timer = setTimeout(() => stop.abort(), timeoutMs);
    const started = performance.now();
    try {
      const res = await relayFetch(apiBase(this.account.server) + "/healthz", { cache: "no-store", signal: stop.signal });
      await res.arrayBuffer();
      if (!res.ok) throw new Error(l10n.t("errors.server_returned", { value: String(res.status) }));
      return Math.round(performance.now() - started);
    } catch (e) {
      throw stop.signal.aborted ? new Error(l10n.t("connection.no_response")) : e;
    } finally {
      clearTimeout(timer);
    }
  }

  async pingMachine(machineId: string, timeoutMs = 15_000): Promise<number> {
    const started = performance.now();
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      await Promise.race([
        this.machineCommand(machineId, { case: "getSettings", value: {} }),
        new Promise<never>((_, reject) => (timer = setTimeout(() => reject(new Error(l10n.t("connection.no_response"))), timeoutMs))),
      ]);
      return Math.round(performance.now() - started);
    } finally {
      clearTimeout(timer);
    }
  }

  async loadDevices(_describe = true): Promise<void> {
    const devPushSeq = this.devicesPushSeq++;
    try {
      await this.ensureHost().securityClient.syncNow(false, this.token);
      const res = await this.api.listDevices({});
      this.maybeDescribeSelf(res.devices);
      const synced = new Promise<void>((resolve) => {
        this.devicesSyncWaiters.push(resolve);
      });
      this.ensureHost().dispatchEvent(
        create(CoreEventSchema, {
          event: {
            case: "devicesFetched",
            value: create(DevicesFetchedEventSchema, {
              opId: 0n,
              pushSeq: devPushSeq,
              wireDevices: res.devices,
            }),
          },
        }),
      );
      await synced;
      this.devicesStatus = "ready";
      this.changed();
    } catch (e) {
      this.devicesStatus = "error";
      this.ensureHost().dispatchEvent(
        create(CoreEventSchema, {
          event: {
            case: "devicesFetched",
            value: create(DevicesFetchedEventSchema, {
              opId: 0n,
              pushSeq: devPushSeq,
              error: errorText(e) || l10n.t("settings.could_not_read_the_device_list"),
            }),
          },
        }),
      );
      this.changed();
    }
  }

  async getSecurityStatus(): Promise<SecurityWorkflowStatus> {
    const st = await this.ensureHost().securityClient.getStatus();
    this.applySecurityStatusSnapshot(st);
    return st;
  }

  async renewLeaseNow(): Promise<SecurityWorkflowStatus> {
    const st = await this.ensureHost().securityClient.renewLease(true);
    this.applySecurityStatusSnapshot(st);
    return st;
  }

  prepareRecoveryRotation() {
    return this.ensureHost().securityClient.prepareRecoveryRotation();
  }

  confirmRecoveryRotation() {
    return this.ensureHost().securityClient.confirmRecoveryRotation();
  }

  prepareHandoffCode(storagePersisted: boolean) {
    return this.ensureHost().securityClient.prepareHandoffCode(storagePersisted);
  }

  publishHandoffOffer() {
    return this.ensureHost().securityClient.publishHandoffOffer();
  }

  discardPendingOp(abandonHandoff = false) {
    return this.ensureHost().securityClient.discardPendingOp(abandonHandoff);
  }

  completeAccountPublicProfile(code: string) {
    return this.ensureHost().securityClient.completeAccountPublicProfile(this.account.server, code);
  }

  startPairing(webOrigin: string, replacesDeviceId = ""): Promise<SecurityPairingInfo> {
    return this.ensureHost().securityClient.startPairing(webOrigin, replacesDeviceId);
  }

  pollPairing(pairingId: string): Promise<SecurityPairingInfo> {
    return this.ensureHost().securityClient.pollPairing(pairingId);
  }

  approvePairing(pairingId: string, sasConfirmed: boolean, removeDeviceIds: string[] = []): Promise<SecurityPairingInfo> {
    return this.ensureHost().securityClient.approvePairing(pairingId, sasConfirmed, removeDeviceIds);
  }

  finishPairingDelivery(pairingId: string): Promise<SecurityPairingInfo> {
    return this.ensureHost().securityClient.finishPairingDelivery(pairingId);
  }

  cancelPairing(pairingId: string): Promise<void> {
    return this.ensureHost().securityClient.cancelPairing(pairingId);
  }

  async handoffCandidates(): Promise<SecurityHandoffCandidate[]> {
    await this.loadDevices(false);
    return this.ensureHost().securityClient.listHandoffCandidates();
  }

  async handoffAuthority(incoming: string) {
    await this.ensureHost().securityClient.handoffAuthority(incoming);
    await this.loadDevices(false);
    this.hostBridge?.retryList();
  }

  async resumeSecurityOperation() {
    await this.ensureHost().securityClient.resumePendingOp();
    if (!this.closed) await this.loadDevices(false);
    this.hostBridge?.retryList();
  }

  async removeDevice(id: string) {
    await this.ensureHost().securityClient.removeDevice(id);
    await this.loadDevices(false);
    this.hostBridge?.retryList();
  }

  async deleteMachineRecords(id: string) {
    await this.ensureHost().securityClient.deleteMachineRecords(id);
    this.hostBridge?.retryList();
  }

  renameDevice(id: string, name: string): Promise<void> {
    if (id !== this.account.deviceId) throw new Error(l10n.t("errors.names_can_only_be_changed_on_that_device"));
    return this.ensureHost().securityClient.updateDeviceMeta({ deviceId: id, name: name.trim() }).then(async () => {
      if (!this.closed) await this.loadDevices(false);
    });
  }

  private async describeSelf() {
    if (this.describingSelf) return;
    this.describingSelf = true;
    try {
      await this.ensureHost().securityClient.updateDeviceMeta({
        platform: "web",
        description: typeof navigator === "undefined" ? "" : describeBrowser(navigator.userAgent),
        describeSelfOnly: true,
      });
      if (!this.closed) await this.loadDevices(false);
    } catch {
      /* Will retry on subsequent devices refresh. */
    } finally {
      this.describingSelf = false;
    }
  }
}

export function errorText(e: unknown): string {
  if (e instanceof SecurityWorkflowError) return e.message;
  if (e instanceof SecurityError) {
    if (e.code === "lease") return l10n.t("errors.remote_control_expired_saving_changes_session_control_and_new");
    if (e.code === "unavailable") return l10n.t("errors.waiting_for_the_owning_computer_to_update_the_current_remote");
    if (e.code === "invalid" || e.code === "untrusted") return l10n.t("errors.invalid_ciphertext_signature_key_or_content_verification_failed");
    if (e.code === "conflict") return l10n.t("errors.membership_chain_conflict_security_operations_stopped_verify_devices_and");
  }
  if (e instanceof CommandError) return e.message;
  if (e instanceof ConnectError) {
    if (e.code === Code.PermissionDenied && e.rawMessage === "login failed") {
      return l10n.t("errors.the_server_does_not_recognize_this_device_it_may");
    }
    return e.rawMessage || e.message;
  }
  if (e instanceof Error) return e.message;
  return String(e);
}

function equalBytes(a: Uint8Array, b: Uint8Array): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i]);
}

export { translateSecurityErrorMessage };
