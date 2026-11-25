import { DirectExecutor } from "./transport/direct-executor";
import { abortable, ownedRound, PathChanged } from "./transport/recovery";
import { cleanup } from "./cleanup";
import { Link as DeviceLink } from "./transport/link";
import { RelayPath } from "./transport/relay-path";
import type { FrameStream } from "./transport/path";
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
import {
  CacheBootstrapLoadedEventSchema,
  CacheEventsLoadedEventSchema,
  CommandOutcomeEventSchema,
  DirectEventsReceivedEventSchema,
  EventSchema as CoreEventSchema,
  OutcomeSource,
  TokenResultEventSchema,
  type CacheBootstrapLoadedEvent,
  type CacheDeleteEffect,
  type CacheEventsLoadedEvent,
  type CacheWriteEffect,
  type CommandOutcomeEvent,
  type DispatchCommandEffect,
  type Effect,
  type TokenResultEvent,
} from "./gen/remoteagent/core/v1/app_pb";
import {
  RelayRpcMethod,
  type RelayRpcCancelEffect,
  type RelayRpcEffect,
} from "./gen/remoteagent/core/v1/relay_pipe_pb";
import {
  SecurityBootstrapKind,
  SecurityErrorCode,
  SecurityKeyKind,
  SecurityOpenEnvelopeOpSchema,
  SecuritySealEnvelopeOpSchema,
  type SecurityPairingInfo,
  type SecurityStatusChangedEffect,
  type SecurityTokenUpdatedEffect,
  type SecurityWorkflowStatus,
} from "./gen/remoteagent/core/v1/security_pb";
import {
  TerminalDirectCloseEventSchema,
  TerminalDirectConfirmRebuildActionSchema,
  TerminalDirectInputEventSchema,
  TerminalDirectLegEventSchema,
  TerminalDirectOpenEventSchema,
  TerminalDirectOutputEventSchema,
  type DirectDcReadStatsEffect,
  type DirectPcAddCandidatesEffect,
  type DirectPcCloseEffect,
  type DirectPcConfirmEffect,
  type DirectPcCreateEffect,
  type DirectPcDescribeEffect,
  type DirectPcSendProbeEffect,
  type DirectPcSetRemoteEffect,
  type DirectSignalSendEffect,
  type TerminalDirectAttachRelayEffect,
  type TerminalDirectDeliverEffect,
  type TerminalDirectSendEffect,
  type TerminalDirectStateEffect,
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
import type { WatchedSessionRoute } from "./gen/remoteagent/core/v1/connection_view_pb";
import { ScreenTarget } from "./gen/remoteagent/core/v1/app_pb";
import type { SharedFileStore } from "./shared-file-store";
import { dispatchSecurityEffect, WebSecurityExecutor } from "./core/security-executor";

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
  GetImageRequestSchema,
  GetImageResponseSchema,
  ListMachinesResponseSchema,
  ListRemoteSessionsResponseSchema,
  NotifyKind,
  RelayErrorDetailSchema,
  RelayErrorReason,
  StoredEventSchema,
  TermHelloSchema,
  AccountPrefsObjectSchema,
  type AccountPrefsObject,
  type Machine as RemoteMachine,
  type RemoteSession,
  type StoredEvent,
} from "./gen/remoteagent/v1/relay_pb";
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
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { relayFetch } from "./transport";
import { closeScheduleNotification, watchSecurityPush } from "./push";
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
  ImageFormat,
} from "./gen/remoteagent/v1/event_pb";
import { matchesFormat, mimeOf } from "./imageview";
import {
  CommandResultSchema,
  ImagePayloadSchema,
  TerminalFrameSchema,
  type TerminalFrame,
} from "./gen/remoteagent/v1/remote_pb";
import { CACHE_PAGE_SIZE, eventCache, type CachedEvent, type EventCache } from "./eventcache";
import { RELAY_PROTOCOL_VERSION } from "./protocol";
import { type StoredAccount } from "./storage";

export function apiBase(server: string): string {
  const origin = checkServer(server);
  return import.meta.env.VITE_SAME_ORIGIN_API ? location.origin : origin;
}

export const TermConfirmKind = { Input: 1, Restart: 2 } as const;
export type TermConfirmKind = (typeof TermConfirmKind)[keyof typeof TermConfirmKind];
export type TermStateEvent = { kind: "confirm"; confirmKind: TermConfirmKind } | { kind: "rebuild" };
export interface TermConn { send(frame: MessageInitShape<typeof TerminalFrameSchema>): void; close(): void; confirmRebuild?: () => void; }

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

type CacheWritePayload = { kind: "machine"; raw: RemoteMachine } | { kind: "session"; raw: RemoteSession } | { kind: "events"; sessionId: string; raw: CachedEvent[] } | { kind: "prefs"; raw: AccountPrefsObject };

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

export class Remote implements CoreExecutor {
  private hostBridge?: HostBridge;

  private persistedMachines = new Map<string, RemoteMachine>();
  private persistedSessions = new Map<string, RemoteSession>();

  private watchedSessions = new Map<string, string>();
  private securityStatusSnapshot?: SecurityWorkflowStatus;
  authorityDeviceId = "";
  authorityError = "";

  directExec: DirectExecutor;
  readonly sharedFileStore?: SharedFileStore;
  readonly attachmentStore?: AttachmentStore;
  prepareImageOverride?: ImagePrepareFn;
  private readonly secStore: SecurityStoreV4;
  private readonly secExec: WebSecurityExecutor;
  private readonly fetchFn: typeof fetch;
  readonly deviceLink: DeviceLink;

  private unwatchSecurityPush?: () => void;
  private token = "";
  private tokenExpires = 0;
  private tokenWarmed = false;
  private loggingIn?: Promise<void>;
  private loginController?: AbortController;
  private tokenWrites: Promise<void> = Promise.resolve();
  private networkGeneration = 0;
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

  private imageCache = new Map<string, ImageResult>();

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
    this.fetchFn = fetchFn;
    if (initialStatus) {
      this.applySecurityStatusSnapshot(initialStatus);
    }
    this.secExec = new WebSecurityExecutor({
      store,
      fetch: fetchFn,
      defaultServer: account.server,
      emit: (ev) => this.hostBridge?.dispatchEvent(ev),
    });
    this.deviceLink = new DeviceLink(new RelayPath());
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
      onTerminalOutput: (machineId, pcId, env, header) => {
        this.terminalStreams.get(header.requestId)?.onInboundEnvelope(machineId, env, true, pcId);
      },
    });
  }

  private readonly terminalStreams = new Map<string, {
    machineId: string;
    sessionId: string;
    sendRelay: (raw: Uint8Array<ArrayBuffer>) => void;
    deliver: (payloads: Uint8Array[]) => void;
    attachRelay: (legId: Uint8Array) => void;
    onState: (state: number, reason: string, needsConfirmation: boolean) => void;
    onInboundEnvelope: (machineId: string, env: Envelope, fromDirect: boolean, pcId: bigint) => void;
  }>();

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
    this.ensureHost().start();
    void this.watchPairingDelivery();
  }

  stop() {
    this.closed = true;
    this.directExec.close();
    this.unwatchSecurityPush?.();
    this.unwatchSecurityPush = undefined;
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
    this.ensureHost().setForeground(active);
  }

  setNetwork(available: boolean, signature?: string) {
    this.network = available;
    this.ensureHost().setNetwork(available, signature);
    this.changed(true);
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

  retryList() {
    this.ensureHost().retryList();
  }

  retryPrefs() {
    this.ensureHost().retryPrefs();
  }

  setWatchedSessions(routes: WatchedSessionRoute[]) {
    this.watchedSessions.clear();
    for (const r of routes) {
      if (r.sessionId) this.watchedSessions.set(r.sessionId, r.machineId);
    }
  }

  protected sessionMachineId(sid: string): string {
    return this.watchedSessions.get(sid) || this.hostBridge?.viewSession(sid).header?.machineId || "";
  }

  private handleDirectEvent(machineId: string, pcId: bigint, env: Envelope, header: Header, _size: number) {
    if (this.closed) return;
    const sid = header.sessionId;
    if (!sid || header.seq <= 0n || !this.watchedSessions.has(sid)) return;
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
    const host = this.ensureHost();
    host.changeProjectPref(kind, path);
    if (!host.viewHome(1).prefsBanner?.syncing) {
      return Promise.resolve(true);
    }
    return new Promise<boolean>((resolve) => {
      const unsub = host.subscribe(ScreenTarget.HOME, () => {
        if (!host.viewHome(1).prefsBanner?.syncing) {
          unsub();
          resolve(true);
        }
      });
    });
  }

  get removed(): boolean { return Boolean(this.hostBridge?.viewConnection().removed || this.securityStatusSnapshot?.removed || this.removalVerified); }
  get securityStatus(): SecurityWorkflowStatus | undefined { return this.securityStatusSnapshot; }
  get securityClient(): SecurityClient { return this.ensureHost().securityClient; }
  demoFlag(_name: string): boolean { return false; }

  // --- CoreExecutor: Stream / Auth / Security / Cache / Relay Pipe ---

  useRelayPipe(): boolean { return true; }

  relayRpc(eff: RelayRpcEffect) {
    void this.secExec.relayRpc(eff);
  }

  relayRpcCancel(eff: RelayRpcCancelEffect) {
    this.secExec.relayRpcCancel(eff);
  }

  openStreamLeg(legId: bigint, _token: string, callbacks: StreamLegCallbacks, helloFrame?: Uint8Array<ArrayBuffer>) {
    this.closeStreamLeg(legId);
    const ws = this.deviceLink.openPathStream(apiBase(this.account.server).replace(/^http/, "ws") + "/v1/stream");
    const state = { ws, stopped: false };
    this.legs.set(legId, state);

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
      try {
        if (!helloFrame || helloFrame.byteLength === 0) throw new Error("missing stream hello frame");
        ws.send(helloFrame);
        this.directExec.attachLeg(ws);
      } catch (e) {
        drop(errorText(e));
      }
    };
    ws.onError = () => drop(l10n.t("errors.disconnected_from_server"));
    ws.onEnd = (code) => drop(l10n.t("errors.disconnected_from_server"), code);
    ws.onFrame = (data) => {
      if (state.stopped || this.legs.get(legId) !== state) return;
      if (data.byteLength > 2 * 1024 * 1024) { drop(l10n.t("errors.push_backlog_reconnecting")); return; }
      callbacks.onRawFrame(legId, new Uint8Array(data));
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

      const [keptMachines, keptSessions, keptPrefs] = await Promise.all([
        this.cache.get("machines"), this.cache.get("sessions"), this.cache.get("prefs"),
      ]);
      const valid = (v: unknown): v is Record<string, unknown> =>
        !!v && typeof v === "object" && (v as { deviceId?: string }).deviceId === this.account.deviceId && (v as { server?: string }).server === this.account.server;

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
    }
  }

  async deleteCache(eff: CacheDeleteEffect): Promise<void> {
    switch (eff.kind.case) {
      case "dropEventsSessionId": await this.cache?.drop(eff.kind.value); break;
      case "removeSessionId":
        await this.cache?.drop(eff.kind.value);
        this.persistedSessions.delete(eff.kind.value);
        this.scroll.delete(eff.kind.value);
        await this.persistSessionsList();
        break;
      case "unlistSessionId": this.persistedSessions.delete(eff.kind.value); await this.persistSessionsList(); break;
      case "removeMachineId": this.persistedMachines.delete(eff.kind.value); await this.persistMachinesList(); break;
    }
  }

  private persistMachinesList = () => this.keepValue("machines", { bytes: toBinary(ListMachinesResponseSchema, create(ListMachinesResponseSchema, { machines: [...this.persistedMachines.values()] })) });
  private persistSessionsList = () => this.keepValue("sessions", { bytes: toBinary(ListRemoteSessionsResponseSchema, create(ListRemoteSessionsResponseSchema, { sessions: [...this.persistedSessions.values()] })) });

  // --- CoreExecutor: Direct DataChannel Command Dispatch (relay legs run in Core) ---

  directSignalSend(eff: DirectSignalSendEffect) { this.directExec.signalSend(eff); }
  directPcCreate(eff: DirectPcCreateEffect) { this.directExec.pcCreate(eff); }
  directPcDescribe(eff: DirectPcDescribeEffect) { void this.directExec.pcDescribe(eff); }
  directPcSetRemote(eff: DirectPcSetRemoteEffect) { void this.directExec.pcSetRemote(eff); }
  directPcAddCandidates(eff: DirectPcAddCandidatesEffect) { void this.directExec.pcAddCandidates(eff); }
  directPcConfirm(eff: DirectPcConfirmEffect) { this.directExec.pcConfirm(eff); }
  directPcClose(eff: DirectPcCloseEffect) { this.directExec.pcClose(eff); }
  directPcSendProbe(eff: DirectPcSendProbeEffect) { this.directExec.pcSendProbe(eff); }
  directDcReadStats(eff: DirectDcReadStatsEffect) { void this.directExec.dcReadStats(eff); }
  terminalDirectSend(eff: TerminalDirectSendEffect) {
    const raw = eff.sealedEnvelope as Uint8Array<ArrayBuffer>;
    if (eff.sendRelay) this.terminalStreams.get(eff.streamId)?.sendRelay(raw);
    if (eff.directPcId > 0n) this.directExec.sendTerminalFrame(eff.directPcId, raw);
  }
  terminalDirectDeliver(eff: TerminalDirectDeliverEffect) {
    this.terminalStreams.get(eff.streamId)?.deliver(eff.payloads);
  }
  terminalDirectAttachRelay(eff: TerminalDirectAttachRelayEffect) {
    this.terminalStreams.get(eff.streamId)?.attachRelay(eff.legId);
  }
  terminalDirectState(eff: TerminalDirectStateEffect) {
    this.terminalStreams.get(eff.streamId)?.onState(eff.state, eff.reason, eff.needsConfirmation);
  }

  async executeCommandAttempt(eff: DispatchCommandEffect, signal?: AbortSignal): Promise<CommandOutcomeEvent> {
    const ticketId = eff.directTicketId;
    const isDirect = eff.routeLeg === CommandRouteLeg.DIRECT && eff.directPcId > 0n;
    const base = { requestId: eff.requestId, attemptId: eff.attemptId, directTicketId: ticketId };
    const mk = (fields: Omit<Partial<CommandOutcomeEvent>, "$typeName" | "requestId" | "attemptId" | "directTicketId">) =>
      create(CommandOutcomeEventSchema, { ...base, ...fields });
    if (this.closed) {
      return mk({ source: OutcomeSource.LOCAL, notForwarded: true, transportCode: "canceled", routeWasDirect: isDirect });
    }
    if (!isDirect || !eff.sealedEnvelope) {
      return mk({
        source: OutcomeSource.LOCAL,
        notForwarded: !eff.retryOfUnknown,
        transportCode: eff.retryOfUnknown ? "unknown" : "failed_precondition",
        transportDetail: !eff.sealedEnvelope ? "missing sealed command" : "relay leg handled by core",
        routeWasDirect: isDirect,
      });
    }
    let handedOff = false;
    const handoff = () => { handedOff = true; };
    const routeStarted = performance.now();
    try {
      signal?.throwIfAborted();
      const env = await this.directExec.sendRpc(
        eff.directPcId,
        eff.requestId,
        toBinary(EnvelopeSchema, eff.sealedEnvelope) as Uint8Array<ArrayBuffer>,
        eff.directTimeoutMs,
        handoff,
        signal,
      );
      return mk({
        source: OutcomeSource.DAEMON,
        attemptBound: true,
        sealedResult: env,
        handedOff,
        routeWasDirect: true,
        elapsedMs: performance.now() - routeStarted,
      });
    } catch (e) {
      const elapsedMs = performance.now() - routeStarted;
      if (e instanceof PathChanged) {
        return mk({ source: OutcomeSource.TRANSPORT, notForwarded: !handedOff, handedOff, transportCode: "path_changed", routeWasDirect: true, elapsedMs });
      }
      return mk({
        source: OutcomeSource.TRANSPORT,
        notForwarded: !handedOff,
        handedOff,
        transportCode: !this.network ? "network" : "unknown",
        transportDetail: errorText(e),
        routeWasDirect: true,
        elapsedMs,
      });
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

  private async sealSecureMessage<D extends DescMessage>(
    h: Omit<Partial<Header>, "$typeName">,
    schema: D, msg: MessageShape<D>,
  ): Promise<{ envelope: Envelope; requestKeyHash: Uint8Array<ArrayBuffer>; payloadHash: Uint8Array<ArrayBuffer> }> {
    await this.ensureWakeSecurity();
    let purpose = purposeOf(h.kind ?? PayloadKind.UNSPECIFIED);
    if (h.kind === PayloadKind.TERMINAL_INPUT) {
      const f = msg as unknown as { kind: { case?: string } };
      if (f.kind.case === "resize") purpose = Purpose.TerminalResize;
      else if (f.kind.case === "historyRequest") purpose = Purpose.TerminalHistory;
    }
    const useSessionSeed = purpose === Purpose.TerminalInput || purpose === Purpose.TerminalResize || purpose === Purpose.TerminalHistory;
    const plaintext = toBinary(schema, msg) as Uint8Array<ArrayBuffer>;
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

  private roundBudget(): number { return this.hostBridge?.viewConnection().liveLoaded || this.networkGeneration > 0 ? 3000 : SETUP_IDLE_MS; }

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
    const auth = createClient(AuthService, createConnectTransport({ fetch: this.fetchFn, baseUrl: apiBase(this.account.server), useBinaryFormat: true, defaultTimeoutMs: OPEN_TIMEOUT_MS }));
    for (let again = true; ; again = false) {
      try {
        const timeoutMs = this.hostBridge?.viewConnection().liveLoaded || this.networkGeneration > 0 ? 3000 : OPEN_TIMEOUT_MS;
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

  private keepValue(name: string, value: Record<string, unknown> | undefined) {
    return this.cache?.set(name, value && { ...value, deviceId: this.account.deviceId, server: this.account.server }).catch(() => undefined);
  }

  // --- Schedules / Images / Terminal / Devices ---

  async readScheduleCache(opId: bigint, machineId: string): Promise<ScheduleCacheLoadedEvent> {
    if (!machineId || this.closed) {
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

  async writeScheduleCache(machineId: string, requestId: string, sealedResultBytes: Uint8Array): Promise<void> {
    if (!machineId || this.closed || !sealedResultBytes.length) return;
    await this.keepValue("schedules:" + machineId, { bytes: sealedResultBytes, requestId });
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

  async openTerminal(
    sid: string,
    onFrame: (f: TerminalFrame) => void | Promise<void>,
    onClose: (reason: string) => void,
    signal?: AbortSignal,
    onStateChange?: (ev: TermStateEvent) => void,
  ): Promise<TermConn> {
    if (this.closed) throw new Error(l10n.t("errors.terminal_connection_closed"));
    const sessionMachineId = this.sessionMachineId(sid);
    if (!sessionMachineId) throw new Error(l10n.t("session.android_unknown"));
    const s = { machineId: sessionMachineId };
    if (signal?.aborted) throw new Error(l10n.t("errors.terminal_connection_canceled"));
    const streamId = newRequestID();
    const termUrl = apiBase(this.account.server).replace(/^http/, "ws") + "/v1/term";
    let activeWs = this.deviceLink.openPathStream(termUrl);
    let activeLegId: Uint8Array = new TextEncoder().encode(streamId);
    let closed = false;
    let openingTimer: ReturnType<typeof setTimeout> | undefined;
    let rejectOpening: (e: Error) => void = () => {};
    const abort = () => finish(l10n.t("errors.terminal_connection_canceled"));
    const stream = this.deviceLink.openStream(onClose, (reason) => {
      closed = true;
      clearTimeout(openingTimer);
      signal?.removeEventListener("abort", abort);
      this.terminalStreams.delete(streamId);
      this.hostBridge?.dispatchEvent(create(CoreEventSchema, {
        event: { case: "terminalDirectClose", value: create(TerminalDirectCloseEventSchema, { streamId }) },
      }));
      rejectOpening(new Error(reason));
      activeWs.close();
    });
    const finish = (reason: string, notify = true) => stream.end(reason, notify);
    signal?.addEventListener("abort", abort, { once: true });
    await new Promise<void>((resolve, reject) => {
      rejectOpening = reject;
      openingTimer = setTimeout(() => finish(l10n.t("errors.terminal_connection_timed_out_reconnect")), OPEN_TIMEOUT_MS);
      activeWs.onOpen = () => {
        if (closed) return;
        clearTimeout(openingTimer);
        try {
          activeWs.send(toBinary(TermHelloSchema, create(TermHelloSchema, { token: this.token, streamId, machineId: s.machineId })));
          resolve();
        } catch (e) {
          finish(errorText(e));
        }
      };
      activeWs.onError = () => finish(l10n.t("errors.cannot_connect_to_terminal"));
      activeWs.onEnd = () => finish(l10n.t("errors.terminal_connection_closed"));
    });
    let queue = Promise.resolve();
    let deliverQueue = Promise.resolve();
    let pendingBytes = 0;
    let pendingFrames = 0;

    const hashBytes = async (data: Uint8Array): Promise<Uint8Array<ArrayBuffer>> => {
      const digest = await crypto.subtle.digest("SHA-256", data as Uint8Array<ArrayBuffer>);
      return new Uint8Array(digest);
    };

    const enqueueInboundEnvelope = (env: Envelope, fromDirect: boolean, pcId: bigint, size: number) => {
      if (closed) return;
      pendingBytes += size;
      pendingFrames++;
      if (pendingBytes > 2 * 1024 * 1024 || pendingFrames > 512) {
        finish(l10n.t("errors.terminal_output_backlog_reconnect_for_the_latest_display"));
        activeWs.close();
        return;
      }
      queue = queue.then(async () => {
        try {
          if (closed) return;
          const { header, msg } = await this.openSecureMessage(
            env,
            { kind: PayloadKind.TERMINAL_OUTPUT, machineId: s.machineId, sessionId: sid, requestId: streamId },
            TerminalFrameSchema,
          );
          if (closed) return;
          const payloadBytes = toBinary(TerminalFrameSchema, msg) as Uint8Array<ArrayBuffer>;
          const frameHash = await hashBytes(payloadBytes);
          if (this.hostBridge) {
            this.hostBridge.dispatchEvent(create(CoreEventSchema, {
              event: {
                case: "terminalDirectOutput",
                value: create(TerminalDirectOutputEventSchema, {
                  streamId,
                  seq: header.seq,
                  frameHash,
                  payload: payloadBytes,
                  fromDirect,
                  pcId,
                }),
              },
            }));
          } else {
            await onFrame(msg);
          }
        } catch (e) {
          if (!fromDirect) {
            finish(errorText(e));
            activeWs.close();
          }
        } finally {
          pendingBytes -= size;
          pendingFrames--;
        }
      });
    };

    const bindRelaySocket = (sock: typeof activeWs, legBytes: Uint8Array) => {
      sock.onFrame = (data) => {
        if (closed) return;
        try {
          const env = fromBinary(EnvelopeSchema, new Uint8Array(data));
          enqueueInboundEnvelope(env, false, 0n, data.byteLength);
        } catch (e) {
          finish(errorText(e));
          sock.close();
        }
      };
      sock.onEnd = () => {
        if (closed || activeWs !== sock) return;
        if (this.hostBridge) {
          this.hostBridge.dispatchEvent(create(CoreEventSchema, {
            event: {
              case: "terminalDirectLeg",
              value: create(TerminalDirectLegEventSchema, { streamId, legId: legBytes, direct: false, open: false }),
            },
          }));
        } else {
          finish(l10n.t("errors.terminal_connection_dropped"));
        }
      };
    };

    this.terminalStreams.set(streamId, {
      machineId: s.machineId,
      sessionId: sid,
      sendRelay: (raw) => {
        if (!closed && activeWs.ready) activeWs.send(raw);
      },
      deliver: (payloads) => {
        for (const p of payloads) {
          const pSize = p.byteLength;
          pendingBytes += pSize;
          pendingFrames++;
          deliverQueue = deliverQueue.then(async () => {
            try {
              if (closed) return;
              await onFrame(fromBinary(TerminalFrameSchema, p));
            } finally {
              pendingBytes -= pSize;
              pendingFrames--;
            }
          });
        }
      },
      attachRelay: (legId) => {
        if (closed) return;
        const legStr = new TextDecoder().decode(legId);
        const nextWs = this.deviceLink.openPathStream(termUrl);
        let settled = false;
        const reportDialFailed = () => {
          if (settled || closed) return;
          settled = true;
          nextWs.close();
          this.hostBridge?.dispatchEvent(create(CoreEventSchema, {
            event: {
              case: "terminalDirectLeg",
              value: create(TerminalDirectLegEventSchema, { streamId, legId, direct: false, open: false }),
            },
          }));
        };
        nextWs.onOpen = () => {
          if (closed) { nextWs.close(); return; }
          try {
            nextWs.send(toBinary(TermHelloSchema, create(TermHelloSchema, { token: this.token, streamId: legStr, machineId: s.machineId })));
            settled = true;
            const old = activeWs;
            activeWs = nextWs;
            activeLegId = legId;
            bindRelaySocket(nextWs, legId);
            if (old !== nextWs) old.close();
            this.hostBridge?.dispatchEvent(create(CoreEventSchema, {
              event: {
                case: "terminalDirectLeg",
                value: create(TerminalDirectLegEventSchema, { streamId, legId, direct: false, open: true }),
              },
            }));
          } catch {
            reportDialFailed();
          }
        };
        nextWs.onError = reportDialFailed;
        nextWs.onEnd = reportDialFailed;
      },
      onState: (state, reason, needsConfirmation) => {
        if (closed) return;
        // M4/M5: NeedsConfirmation enters user confirmation UI without auto-closing;
        // rebuild_confirmed closes the old stream silently and triggers a fresh open.
        if (needsConfirmation) {
          if (onStateChange) {
            onStateChange({ kind: "confirm", confirmKind: state === 4 ? TermConfirmKind.Restart : TermConfirmKind.Input });
            return;
          }
          finish(l10n.t("errors.terminal_connection_dropped"));
          activeWs.close();
          return;
        }
        if (state === 5 && reason === "rebuild_confirmed") {
          finish(l10n.t("errors.terminal_connection_closed"), false);
          activeWs.close();
          onStateChange?.({ kind: "rebuild" });
          return;
        }
        if (state === 5) {
          finish(
            reason === "conflict"
              ? l10n.t("errors.terminal_frames_are_out_of_order")
              : reason === "replay_overflow"
                ? l10n.t("terminal.replay_overflow")
                : l10n.t("errors.terminal_connection_dropped"),
          );
          activeWs.close();
        }
      },
      onInboundEnvelope: (machineId, env, fromDirect, pcId) => {
        if (machineId === s.machineId) enqueueInboundEnvelope(env, fromDirect, pcId, env.ciphertext.byteLength + env.header.byteLength);
      },
    });

    bindRelaySocket(activeWs, activeLegId);
    if (closed) throw new Error(l10n.t("errors.terminal_connection_closed"));
    // M6: register in Core before sending openTerminal RPC so early frames are never lost.
    this.hostBridge?.dispatchEvent(create(CoreEventSchema, {
      event: {
        case: "terminalDirectOpen",
        value: create(TerminalDirectOpenEventSchema, {
          streamId,
          machineId: s.machineId,
          sessionId: sid,
          relayLegId: activeLegId,
        }),
      },
    }));
    try {
      await this.ensureHost().runMachineCommand(s.machineId, sid, { case: "openTerminal", value: { sessionId: sid, streamId } }, signal, true);
    } catch (e) {
      finish(errorText(e));
      throw e;
    }
    if (closed) throw new Error(l10n.t("errors.terminal_connection_closed"));
    // H-B: signal Core that daemon confirmed openTerminal so direct AttachPath fires now.
    this.hostBridge?.dispatchEvent(create(CoreEventSchema, {
      event: {
        case: "terminalDirectLeg",
        value: create(TerminalDirectLegEventSchema, {
          streamId,
          legId: activeLegId,
          direct: false,
          open: true,
        }),
      },
    }));
    let seq = 0n;
    let sending = Promise.resolve();
    return {
      send: (frame) => {
        sending = sending.then(async () => {
          if (closed) return;
          seq++;
          const msg = create(TerminalFrameSchema, frame);
          const env = (await this.sealSecureMessage(
            { kind: PayloadKind.TERMINAL_INPUT, machineId: s.machineId, sessionId: sid, requestId: streamId, seq, sender: this.account.deviceId },
            TerminalFrameSchema,
            msg,
          )).envelope;
          if (closed) return;
          const sealedBytes = toBinary(EnvelopeSchema, env) as Uint8Array<ArrayBuffer>;
          if (this.hostBridge) {
            const frameHash = await hashBytes(toBinary(TerminalFrameSchema, msg));
            this.hostBridge.dispatchEvent(create(CoreEventSchema, {
              event: {
                case: "terminalDirectInput",
                value: create(TerminalDirectInputEventSchema, {
                  streamId,
                  seq,
                  isInput: msg.kind.case === "input",
                  sealedEnvelope: sealedBytes,
                  frameHash,
                }),
              },
            }));
          } else if (activeWs.ready) {
            activeWs.send(sealedBytes);
          }
        });
      },
      close: () => finish(l10n.t("errors.terminal_connection_closed"), false),
      confirmRebuild: () => {
        if (closed) return;
        this.hostBridge?.dispatchEvent(create(CoreEventSchema, {
          event: {
            case: "terminalDirectConfirmRebuild",
            value: create(TerminalDirectConfirmRebuildActionSchema, { streamId }),
          },
        }));
      },
    };
  }

  async getImage(sid: string, imageId: string): Promise<ImageResult> {
    const key = sid + "/" + imageId;
    const hit = this.imageCache.get(key);
    if (hit) { this.imageCache.delete(key); this.imageCache.set(key, hit); return hit; }
    const machineId = this.sessionMachineId(sid);
    if (!machineId) throw new Error(l10n.t("session.android_unknown"));
    let res;
    const raw = await this.secExec.executeUnary({
      opId: 0n,
      method: RelayRpcMethod.GET_IMAGE,
      server: this.account.server,
      authToken: this.token,
      timeoutMs: RPC_TIMEOUT_MS,
      requestBytes: toBinary(GetImageRequestSchema, create(GetImageRequestSchema, { sessionId: sid, imageId })),
    });
    if (raw.connectCode === Code.NotFound) return { kind: "missing" };
    if (raw.connectCode !== 0 || raw.networkError || raw.diagnosticMessage) {
      throw new Error(raw.diagnosticMessage || "getImage failed");
    }
    res = fromBinary(GetImageResponseSchema, raw.responseBytes);
    if (res.expired || !res.image) return { kind: "expired" };
    let out: ImageResult;
    try {
      const { msg } = await this.openSecureMessage(
        res.image,
        { kind: PayloadKind.IMAGE, machineId, sessionId: sid, requestId: imageId },
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
  async pushKey(): Promise<string> {
    return (await this.ensureHost().pushRpc({ getKey: true })).publicKey;
  }
  async setPushSubscription(subscriptionJson: string, kinds: NotifyKind[]): Promise<void> {
    await this.ensureHost().pushRpc({ setWebSubscription: true, subscriptionJson, kinds });
  }

  async pingServer(timeoutMs = 8000, signal?: AbortSignal): Promise<number> {
    const stop = new AbortController();
    let timedOut = false;
    const onExternalAbort = () => stop.abort();
    if (signal?.aborted) stop.abort();
    else signal?.addEventListener("abort", onExternalAbort, { once: true });
    const timer = setTimeout(() => { timedOut = true; stop.abort(); }, timeoutMs);
    const started = performance.now();
    try {
      const res = await relayFetch(apiBase(this.account.server) + "/healthz", { cache: "no-store", signal: stop.signal });
      await res.arrayBuffer();
      if (!res.ok) throw new Error(l10n.t("errors.server_returned", { value: String(res.status) }));
      return Math.round(performance.now() - started);
    } catch (e) {
      throw timedOut ? new Error(l10n.t("connection.no_response")) : e;
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener("abort", onExternalAbort);
    }
  }

  async pingMachine(machineId: string, timeoutMs = 15_000, signal?: AbortSignal): Promise<number> {
    const stop = new AbortController();
    let timedOut = false;
    const onExternalAbort = () => stop.abort();
    if (signal?.aborted) stop.abort();
    else signal?.addEventListener("abort", onExternalAbort, { once: true });
    const timer = setTimeout(() => { timedOut = true; stop.abort(); }, timeoutMs);
    const started = performance.now();
    try {
      await this.ensureHost().runMachineCommand(machineId, "", { case: "getSettings", value: {} }, stop.signal, true);
      return Math.round(performance.now() - started);
    } catch (e) {
      throw timedOut ? new Error(l10n.t("connection.no_response")) : e;
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener("abort", onExternalAbort);
    }
  }

  async loadDevices(_describe = true): Promise<void> {
    try {
      await this.ensureHost().securityClient.syncNow(false, this.token);
      await this.ensureHost().refreshDevices();
      this.devicesStatus = "ready";
      this.changed();
    } catch {
      this.devicesStatus = "error";
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
}

export function errorText(e: unknown): string {
  if (e instanceof SecurityWorkflowError) return e.message;
  if (e instanceof SecurityError) {
    if (e.code === "lease") return l10n.t("errors.remote_control_expired_saving_changes_session_control_and_new");
    if (e.code === "unavailable") return l10n.t("errors.waiting_for_the_owning_computer_to_update_the_current_remote");
    if (e.code === "invalid" || e.code === "untrusted") return l10n.t("errors.invalid_ciphertext_signature_key_or_content_verification_failed");
    if (e.code === "conflict") return l10n.t("errors.membership_chain_conflict_security_operations_stopped_verify_devices_and");
  }
  if (e instanceof ConnectError) {
    const relayReason = e.findDetails(RelayErrorDetailSchema)[0]?.reason;
    if (relayReason === RelayErrorReason.LOGIN_FAILED) {
      return l10n.t("errors.the_server_does_not_recognize_this_device_it_may");
    }
    return e.rawMessage || e.message;
  }
  if (e instanceof Error) return e.message;
  return String(e);
}

export { translateSecurityErrorMessage };
