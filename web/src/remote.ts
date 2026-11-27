import { DirectExecutor } from "./transport/direct-executor";
import { PathChanged } from "./transport/recovery";
import { cleanup } from "./cleanup";
import { Link as DeviceLink } from "./transport/link";
import { RelayPath } from "./transport/relay-path";
import type { FrameStream } from "./transport/path";
import * as l10n from "./i18n";
import type { ImagePrepareFn } from "./attachment-executor";
import type { AttachmentStore } from "./attachment-store";
import {
  clearLoginToken,
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
  type CoreHost,
  type HostBridge,
  type StreamLegCallbacks,
} from "./core/host";
export { SecurityError };
export type { StreamLegCallbacks } from "./core/host";
import {
  SecurityClient,
  SecurityWorkflowError,
  translateSecurityErrorMessage,
} from "./core/security-client";
import {
  CacheBootstrapLoadedEventSchema,
  CacheEventsLoadedEventSchema,
  CommandOutcomeEventSchema,
  DirectEventsReceivedEventSchema,
  EventSchema as CoreEventSchema,
  OutcomeSource,
  type CacheBootstrapLoadedEvent,
  type CacheDeleteEffect,
  type CacheEventsLoadedEvent,
  type CacheWriteEffect,
  type CommandOutcomeEvent,
  type DispatchCommandEffect,
  type Effect,
} from "./gen/remoteagent/core/v1/app_pb";
import {
  SessionImageLoadStatus,
  TerminalLifecycleState,
  type RelayRpcCancelEffect,
  type RelayRpcEffect,
  type TerminalCloseSocketEffect,
  type TerminalDeliverOutputEffect,
  type TerminalOpenSocketEffect,
  type TerminalStateChangedEffect,
} from "./gen/remoteagent/core/v1/relay_pipe_pb";
import { renderText } from "./core/text";
import {
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
  DirectPcSendProbeEffect,
  DirectPcSetRemoteEffect,
  DirectSignalSendEffect,
  TerminalDirectSendEffect,
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

import {
  ListMachinesResponseSchema,
  ListRemoteSessionsResponseSchema,
  MachineSchema,
  NotifyKind,
  RelayErrorDetailSchema,
  RelayErrorReason,
  RemoteSessionSchema,
  StoredEventSchema,
  type Machine as RemoteMachine,
  type RemoteSession,
} from "./gen/remoteagent/v1/relay_pb";
import {
  create,
  fromBinary,
  toBinary,
  type MessageInitShape,
} from "@bufbuild/protobuf";
import {
  ConnectError,
} from "@connectrpc/connect";
import { relayFetch } from "./transport";
import { closeScheduleNotification, watchSecurityPush } from "./push";
import { checkServer } from "./crypto";
import {
  EnvelopeSchema,
  type Envelope,
  type Header,
} from "./gen/remoteagent/v1/envelope_pb";
import {
  ImageFormat,
} from "./gen/remoteagent/v1/event_pb";
import {
  TerminalFrameSchema,
  type TerminalFrame,
} from "./gen/remoteagent/v1/remote_pb";
import { CACHE_PAGE_SIZE, eventCache, type CachedEvent, type EventCache } from "./eventcache";
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
const SESSION_PAGE = 50;
export { SESSION_PAGE };
export const PREFS_OBJECT = "project-prefs";
const SCOPE_STORAGE_KEY = "ra-scope";
const SCOPE_HMAC_KEY_VALUE = "scope-hmac-key";

export type ImageResult = { kind: "ok"; blob: Blob; width: number; height: number; format: ImageFormat } | { kind: "expired" } | { kind: "missing" } | { kind: "bad"; why?: string };

type CacheWritePayload = { kind: "machine"; raw: RemoteMachine } | { kind: "session"; raw: RemoteSession } | { kind: "events"; sessionId: string; raw: CachedEvent[] } | { kind: "prefs"; raw: Uint8Array };

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

  directExec: DirectExecutor;
  readonly sharedFileStore?: SharedFileStore;
  readonly attachmentStore?: AttachmentStore;
  prepareImageOverride?: ImagePrepareFn;
  private readonly secExec: WebSecurityExecutor;
  readonly deviceLink: DeviceLink;

  private unwatchSecurityPush?: () => void;
  private token = "";
  private tokenWrites: Promise<void> = Promise.resolve();
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
    if (initialStatus) {
      this.applySecurityStatusSnapshot(initialStatus);
    }
    this.secExec = new WebSecurityExecutor({
      store,
      fetch: fetchFn,
      defaultServer: account.server,
      defaultDeviceId: account.deviceId,
      emit: (ev) => this.hostBridge?.dispatchEvent(ev),
    });
    this.deviceLink = new DeviceLink(new RelayPath());
    this.directExec = this.newDirectExecutor();
  }

  static async load(
    acct: StoredAccount,
    sharedHost: CoreHost,
    initialStatus?: SecurityWorkflowStatus,
  ): Promise<Remote | undefined> {
    if (!acct.server || !acct.deviceId) return undefined;
    const status = initialStatus ?? sharedHost.securityClient.status;
    const r = new Remote(acct, eventCache(), status);
    sharedHost.attachRemote(r);
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

  protected dispatchToHost(ev: import("./gen/remoteagent/core/v1/app_pb").Event) {
    this.ensureHost().dispatchEvent(ev);
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

  private newDirectExecutor() {
    return new DirectExecutor({
      emit: (ev) => this.hostBridge?.dispatchEvent(ev),
      onPushedEvent: (machineId, pcId, env, header, size) => {
        this.handleDirectEvent(machineId, pcId, env, header, size);
      },
      onTerminalOutput: (machineId, pcId, _env, header, raw) => {
        const s = this.terminalStreams.get(header.requestId);
        if (s && s.machineId === machineId) s.onDirectBytes(raw, pcId);
      },
    });
  }

  private nextTerminalOpId = 1n;
  private readonly pendingTerminalOpens = new Map<bigint, {
    onOpenSocket: (eff: TerminalOpenSocketEffect) => void;
    onStateChanged: (eff: TerminalStateChangedEffect) => void;
  }>();
  private readonly terminalStreams = new Map<string, {
    machineId: string;
    sendRelay: (raw: Uint8Array<ArrayBuffer>) => void;
    onOpenSocket: (eff: TerminalOpenSocketEffect) => void;
    onCloseSocket: (eff: TerminalCloseSocketEffect) => void;
    onDeliver: (eff: TerminalDeliverOutputEffect) => void;
    onStateChanged: (eff: TerminalStateChangedEffect) => void;
    onDirectBytes: (raw: Uint8Array<ArrayBuffer>, pcId: bigint) => void;
  }>();

  start() {
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
  }

  stop() {
    this.closed = true;
    this.directExec.close();
    this.unwatchSecurityPush?.();
    this.unwatchSecurityPush = undefined;
    this.deviceLink.closeStreams(l10n.t("errors.terminal_connection_closed"));
    for (const legId of [...this.legs.keys()]) this.closeStreamLeg(legId);
    this.hostBridge?.stop();
  }

  dispose(disposeHost = true) {
    this.stop();
    this.secExec.close();
    if (disposeHost) {
      disposeCoreHost(this);
    }
    this.hostBridge = undefined;
  }

  private applySecurityStatusSnapshot(status: SecurityWorkflowStatus) {
    const prevRemoved = this.removed;
    this.securityStatusSnapshot = status;
    if (status.isAuthority && status.deviceId) this.authorityDeviceId = status.deviceId;
    if (status.removed && !this.removalVerified) {
      this.removalVerified = true;
      this.clearLocalToken();
      if (!this.closed) this.stop();
    }
    if (this.removed !== prevRemoved || this.hostBridge) {
      this.changed();
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
            rawEnvelopes: [toBinary(EnvelopeSchema, env)],
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

  relayRpc(eff: RelayRpcEffect) {
    void this.secExec.relayRpc(eff);
  }

  relayRpcCancel(eff: RelayRpcCancelEffect) {
    this.secExec.relayRpcCancel(eff);
  }

  openStreamLeg(legId: bigint, token: string, callbacks: StreamLegCallbacks, helloFrame?: Uint8Array<ArrayBuffer>) {
    if (token) this.token = token;
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

  private clearLocalToken() {
    this.token = "";
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
    if (this.closed) return;
    if (eff.cleared) {
      this.clearLocalToken();
      return;
    }
    this.token = eff.token;
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
    const rawMachines: Uint8Array[] = [], rawSessions: Uint8Array[] = [];
    let rawPrefs = new Uint8Array();
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
          rawMachines.push(toBinary(MachineSchema, m));
        }
      }
      if (valid(keptSessions) && keptSessions.bytes instanceof Uint8Array) {
        for (const rs of fromBinary(ListRemoteSessionsResponseSchema, keptSessions.bytes).sessions) {
          this.persistedSessions.set(rs.sessionId, rs);
          rawSessions.push(toBinary(RemoteSessionSchema, rs));
        }
      }
      if (valid(keptPrefs) && keptPrefs.bytes instanceof Uint8Array) {
        rawPrefs = new Uint8Array(keptPrefs.bytes);
      }
      return create(CacheBootstrapLoadedEventSchema, { opId, rawMachines, rawSessions, rawPrefs, homeScopeToken, scopeHmacKey });
    } catch (e) {
      return create(CacheBootstrapLoadedEventSchema, { opId, rawMachines, rawSessions, rawPrefs, homeScopeToken, scopeHmacKey, error: errorText(e) });
    }
  }

  async readEventsCache(opId: bigint, sessionId: string, newest: boolean, afterSeq: bigint, limit: number): Promise<CacheEventsLoadedEvent> {
    if (!this.cache) return create(CacheEventsLoadedEventSchema, { opId, sessionId });
    try {
      const cap = limit > 0 ? limit : CACHE_PAGE_SIZE;
      const rows = newest && this.cache.readNewest ? await this.cache.readNewest(sessionId, cap) : await this.cache.read(sessionId, afterSeq, cap);
      const rawEvents = rows.map((row) =>
        toBinary(StoredEventSchema, create(StoredEventSchema, { seq: row.seq, ...(row.envelope ? { envelope: row.envelope } : {}) })),
      );
      return create(CacheEventsLoadedEventSchema, { opId, sessionId, rawEvents });
    } catch (e) {
      return create(CacheEventsLoadedEventSchema, { opId, sessionId, error: errorText(e) });
    }
  }

  private extractCacheWrite(eff: CacheWriteEffect): CacheWritePayload | undefined {
    switch (eff.kind.case) {
      case "rawWriteMachine": return eff.kind.value.length > 0 ? { kind: "machine", raw: fromBinary(MachineSchema, eff.kind.value) } : undefined;
      case "rawWriteSession":
      case "rawKeepSessionOwner": return eff.kind.value.length > 0 ? { kind: "session", raw: fromBinary(RemoteSessionSchema, eff.kind.value) } : undefined;
      case "writeStoredEvents": {
        if (!this.cache) return undefined;
        const raw: CachedEvent[] = eff.kind.value.rawEvents.flatMap((b) => {
          if (b.length === 0) return [];
          const se = fromBinary(StoredEventSchema, b);
          return se.envelope ? [{ seq: se.seq, envelope: se.envelope }] : [];
        });
        return raw.length > 0 ? { kind: "events", sessionId: eff.kind.value.sessionId, raw } : undefined;
      }
      case "rawWritePrefs": return eff.kind.value.length > 0 ? { kind: "prefs", raw: eff.kind.value } : undefined;
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
      case "prefs": await this.keepValue("prefs", { bytes: payload.raw }); break;
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
  terminalOpenSocket(eff: TerminalOpenSocketEffect) {
    const s = this.terminalStreams.get(eff.streamId);
    if (s) s.onOpenSocket(eff);
    else this.pendingTerminalOpens.get(eff.opId)?.onOpenSocket(eff);
  }
  terminalCloseSocket(eff: TerminalCloseSocketEffect) {
    this.terminalStreams.get(eff.streamId)?.onCloseSocket(eff);
  }
  terminalDeliverOutput(eff: TerminalDeliverOutputEffect) {
    this.terminalStreams.get(eff.streamId)?.onDeliver(eff);
  }
  terminalStateChanged(eff: TerminalStateChangedEffect) {
    const s = this.terminalStreams.get(eff.streamId);
    if (s) s.onStateChanged(eff);
    else this.pendingTerminalOpens.get(eff.opId)?.onStateChanged(eff);
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
    if (!isDirect || eff.rawSealedEnvelope.length === 0) {
      return mk({
        source: OutcomeSource.LOCAL,
        notForwarded: !eff.retryOfUnknown,
        transportCode: eff.retryOfUnknown ? "unknown" : "failed_precondition",
        transportDetail: eff.rawSealedEnvelope.length === 0 ? "missing sealed command" : "relay leg handled by core",
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
        eff.rawSealedEnvelope as Uint8Array<ArrayBuffer>,
        eff.directTimeoutMs,
        handoff,
        signal,
      );
      return mk({
        source: OutcomeSource.DAEMON,
        attemptBound: true,
        rawSealedResult: toBinary(EnvelopeSchema, env),
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

  private keepValue(name: string, value: Record<string, unknown> | undefined) {
    return this.cache?.set(name, value && { ...value, deviceId: this.account.deviceId, server: this.account.server }).catch(() => undefined);
  }

  // --- Schedules / Images / Terminal / Devices ---

  async readScheduleCache(opId: bigint, machineId: string): Promise<ScheduleCacheLoadedEvent> {
    if (!machineId || this.closed) {
      return create(ScheduleCacheLoadedEventSchema, { opId, machineId });
    }
    try {
      const kept = (await this.cache?.get("schedules:" + machineId)) as { blob?: unknown } | undefined;
      if (!kept || !(kept.blob instanceof Uint8Array) || kept.blob.byteLength === 0) {
        return create(ScheduleCacheLoadedEventSchema, { opId, machineId });
      }
      return create(ScheduleCacheLoadedEventSchema, { opId, machineId, rawBlob: new Uint8Array(kept.blob) });
    } catch (e) {
      return create(ScheduleCacheLoadedEventSchema, { opId, machineId, error: errorText(e) });
    }
  }

  async writeScheduleCache(machineId: string, blob: Uint8Array): Promise<void> {
    if (!machineId || this.closed || !blob.length) return;
    await this.keepValue("schedules:" + machineId, { blob });
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
    terminalId = "",
  ): Promise<TermConn> {
    if (this.closed) throw new Error(l10n.t("errors.terminal_connection_closed"));
    if (signal?.aborted) throw new Error(l10n.t("errors.terminal_connection_canceled"));
    const host = this.ensureHost();
    const machineId = this.sessionMachineId(sid);
    const termUrl = apiBase(this.account.server).replace(/^http/, "ws") + "/v1/term";
    const opId = this.nextTerminalOpId++;
    let streamId = "";
    let activeWs: FrameStream | undefined;
    const allSockets = new Set<FrameStream>();
    let closed = false;
    let settled = false;
    let openingTimer: ReturnType<typeof setTimeout> | undefined;
    let resolveOpening: () => void = () => {};
    let rejectOpening: (e: Error) => void = () => {};
    let deliverQueue = Promise.resolve();
    let pendingBytes = 0;
    let pendingFrames = 0;

    const closeAllSockets = () => {
      for (const s of allSockets) s.close();
      allSockets.clear();
      activeWs = undefined;
    };
    const abort = () => finish(l10n.t("errors.terminal_connection_canceled"));
    const stream = this.deviceLink.openStream(onClose, (reason) => {
      closed = true;
      clearTimeout(openingTimer);
      signal?.removeEventListener("abort", abort);
      this.pendingTerminalOpens.delete(opId);
      if (streamId) this.terminalStreams.delete(streamId);
      host.terminalClose(streamId, opId);
      if (!settled) {
        settled = true;
        rejectOpening(new Error(reason));
      }
      closeAllSockets();
    });
    const finish = (reason: string, notify = true) => stream.end(reason, notify);
    signal?.addEventListener("abort", abort, { once: true });

    const reserveBackpressure = (bytes: number): boolean => {
      if (pendingBytes + bytes > 2 * 1024 * 1024 || pendingFrames >= 512) {
        finish(l10n.t("errors.terminal_output_backlog_reconnect_for_the_latest_display"));
        return false;
      }
      pendingBytes += bytes;
      pendingFrames++;
      return true;
    };
    const releaseBackpressure = (frames: number, bytes: number) => {
      pendingFrames = Math.max(0, pendingFrames - frames);
      pendingBytes = Math.max(0, pendingBytes - bytes);
    };

    const openSocket = (eff: TerminalOpenSocketEffect) => {
      if (closed) return;
      const legId = new Uint8Array(eff.legId);
      const sock = this.deviceLink.openPathStream(termUrl);
      allSockets.add(sock);
      if (eff.isInitial) activeWs = sock;
      let sockOpened = false;
      let sockEnded = false;
      const reportClose = (error: string, code?: number) => {
        if (sockEnded) return;
        sockEnded = true;
        allSockets.delete(sock);
        if (closed) return;
        if (sockOpened && activeWs !== sock) return;
        host.terminalSocket({
          streamId: eff.streamId,
          legId,
          closed: true,
          policyViolation: code === 1008,
          error,
        });
      };
      sock.onOpen = () => {
        if (closed) { sock.close(); return; }
        sockOpened = true;
        if (eff.isInitial) clearTimeout(openingTimer);
        try {
          sock.send(new Uint8Array(eff.helloBytes));
          const prev = activeWs;
          activeWs = sock;
          if (prev && prev !== sock) {
            allSockets.delete(prev);
            prev.close();
          }
          host.terminalSocket({ streamId: eff.streamId, legId, opened: true });
        } catch (e) {
          reportClose(errorText(e));
        }
      };
      sock.onFrame = (data) => {
        if (closed || activeWs !== sock) return;
        const raw = new Uint8Array(data);
        if (!reserveBackpressure(raw.byteLength)) return;
        host.terminalInboundBytes(eff.streamId, raw, false, 0n);
      };
      sock.onError = () => reportClose(l10n.t("errors.cannot_connect_to_terminal"));
      sock.onEnd = (code) => reportClose("", code);
    };

    const onDeliver = (eff: TerminalDeliverOutputEffect) => {
      if (closed) return;
      const cf = eff.consumedFrames;
      const cb = eff.consumedBytes;
      const delivered: TerminalFrame[] = [];
      for (const f of eff.frames) {
        const k = f.kind;
        if (k.case === "output") delivered.push(create(TerminalFrameSchema, { kind: { case: "output", value: k.value } }));
        else if (k.case === "snapshot") delivered.push(create(TerminalFrameSchema, { kind: { case: "snapshot", value: { cols: k.value.cols, rows: k.value.rows, data: k.value.data } } }));
        else if (k.case === "closed") delivered.push(create(TerminalFrameSchema, { kind: { case: "closed", value: k.value } }));
      }
      if (delivered.length === 0) {
        releaseBackpressure(cf, cb);
        return;
      }
      deliverQueue = deliverQueue.then(async () => {
        try {
          for (const frame of delivered) {
            if (closed) break;
            await onFrame(frame);
          }
        } finally {
          releaseBackpressure(cf, cb);
        }
      });
    };

    const onStateChanged = (eff: TerminalStateChangedEffect) => {
      if (closed) return;
      if (eff.openSettled && !settled) {
        settled = true;
        clearTimeout(openingTimer);
        if (eff.openOk) { resolveOpening(); return; }
        const msg = renderText(eff.openError ?? eff.reasonText) || l10n.t("errors.terminal_connection_closed");
        finish(msg, false);
        rejectOpening(new Error(msg));
        return;
      }
      if (eff.state === TerminalLifecycleState.CONFIRM_INPUT || eff.state === TerminalLifecycleState.CONFIRM_RESTART) {
        if (onStateChange) onStateChange({ kind: "confirm", confirmKind: eff.state === TerminalLifecycleState.CONFIRM_RESTART ? TermConfirmKind.Restart : TermConfirmKind.Input });
        else finish(l10n.t("errors.terminal_connection_dropped"));
      } else if (eff.state === TerminalLifecycleState.REBUILD) {
        finish(renderText(eff.reasonText) || l10n.t("errors.terminal_connection_closed"), false);
        onStateChange?.({ kind: "rebuild" });
      } else if (eff.state === TerminalLifecycleState.CLOSED) {
        finish(renderText(eff.reasonText) || l10n.t("errors.terminal_connection_dropped"));
      }
    };

    this.pendingTerminalOpens.set(opId, {
      onOpenSocket: (eff) => {
        if (closed) return;
        streamId = eff.streamId;
        this.pendingTerminalOpens.delete(opId);
        this.terminalStreams.set(streamId, {
          machineId,
          sendRelay: (raw) => { if (!closed && activeWs?.ready) activeWs.send(raw); },
          onOpenSocket: openSocket,
          onCloseSocket: () => closeAllSockets(),
          onDeliver,
          onStateChanged,
          onDirectBytes: (raw, pcId) => { if (!closed && reserveBackpressure(raw.byteLength)) host.terminalInboundBytes(streamId, raw, true, pcId); },
        });
        openSocket(eff);
      },
      onStateChanged,
    });

    await new Promise<void>((resolve, reject) => {
      resolveOpening = resolve;
      rejectOpening = reject;
      openingTimer = setTimeout(() => finish(l10n.t("errors.terminal_connection_timed_out_reconnect")), OPEN_TIMEOUT_MS);
      host.terminalOpen(opId, sid, machineId, terminalId);
    });
    if (closed) throw new Error(l10n.t("errors.terminal_connection_closed"));

    return {
      send: (frame) => {
        if (closed || !streamId || !frame.kind) return;
        const k = frame.kind;
        if (k.case === "input") host.terminalInput({ streamId, kind: { case: "input", value: new Uint8Array(k.value) } });
        else if (k.case === "resize") host.terminalInput({ streamId, kind: { case: "resize", value: { cols: k.value.cols ?? 0, rows: k.value.rows ?? 0, fit: Boolean(k.value.fit) } } });
        else if (k.case === "historyRequest") host.terminalInput({ streamId, kind: { case: "history", value: { requestId: k.value.requestId ?? "", cursor: k.value.cursor ?? "" } } });
      },
      close: () => finish(l10n.t("errors.terminal_connection_closed"), false),
      confirmRebuild: () => { if (!closed && streamId) host.terminalConfirmRebuild(streamId); },
    };
  }

  async getImage(sid: string, imageId: string): Promise<ImageResult> {
    const key = sid + "/" + imageId;
    const hit = this.imageCache.get(key);
    if (hit) { this.imageCache.delete(key); this.imageCache.set(key, hit); return hit; }
    const res = await this.ensureHost().fetchSessionImage(sid, imageId, this.sessionMachineId(sid));
    switch (res.status) {
      case SessionImageLoadStatus.OK: {
        const out: ImageResult = {
          kind: "ok",
          blob: new Blob([res.data as Uint8Array<ArrayBuffer>], { type: res.mimeType }),
          width: res.width,
          height: res.height,
          format: res.format,
        };
        this.imageCache.set(key, out);
        while (this.imageCache.size > IMAGE_CACHE) this.imageCache.delete(this.imageCache.keys().next().value!);
        return out;
      }
      case SessionImageLoadStatus.MISSING:
        return { kind: "missing" };
      case SessionImageLoadStatus.EXPIRED:
        return { kind: "expired" };
      case SessionImageLoadStatus.BAD:
        return {
          kind: "bad",
          ...(res.retiredHistory ? { why: renderText(res.errorText) || l10n.t("errors.this_history_uses_an_old_encryption_format_and_can") } : {}),
        };
      default: {
        const msg = renderText(res.errorText) || res.errorCode || l10n.t("errors.command_failed");
        throw new Error(msg);
      }
    }
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
      await this.ensureHost().pingMachine(machineId, stop.signal);
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
