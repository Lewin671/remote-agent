import { DirectExecutor, peerFrameBody } from "./transport/direct-executor";
import { watchSecurityInstall } from "./device-security-store";
import { RESULT_RETENTION_MS } from "./result-retention.gen";
import { abortable, ownedRound, PathChanged } from "./transport/recovery";
import { cleanup } from "./cleanup";
import { Link as DeviceLink } from "./transport/link";
import { RelayPath } from "./transport/relay-path";
import type { FrameStream } from "./transport/path";
import { machineNotificationKinds } from "./machine-notifications";
import * as l10n from "./i18n";
import { UnsupportedHistoryError } from "./crypto";
import { ComposerDraftStore, DraftTexts } from "./composer-drafts";
import { AuthorityLifecycle, PairingDeliveryError, readSecurityOperation } from "./security-operation";
import type { AuthorityClient, HandoffOffer } from "./authority-client";
import type { AuthorityPairing } from "./authority-pairing";
import { clearLoginToken, loadLoginToken, saveLoginToken } from "./device-security-store";
import {
  CommandRouteLeg,
  disposeCoreHost,
  getCoreHost,
  type CoreExecutor,
  type HostBridge,
  type StreamLegCallbacks,
} from "./core/host";
export type { StreamLegCallbacks } from "./core/host";
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
  type Event as CoreEvent,
  type RpcEffect,
  type SecuritySnapshotEvent,
  type TokenResultEvent,
} from "./gen/remoteagent/core/v1/app_pb";
import {
  SecurityOpenEnvelopeOpSchema,
  SecurityRecordEventSchema,
  SecuritySealEnvelopeOpSchema,
  type SecurityCryptoBatchEffect,
  type SecurityKeyAgreeEffect,
  type SecurityKeySignEffect,
  type SecurityRecordEvent,
} from "./gen/remoteagent/core/v1/security_pb";
import type {
  DirectDcReadStatsEffect,
  DirectKeyAgreeEffect,
  DirectKeySignEffect,
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
import type { SecurityRecord } from "./device-security-store";
import { machineName } from "./model";
import { DEFAULT_AGENT } from "./agents";
import type { SharedFileChunkResult } from "./shared-files";
import { WebSecurityExecutor } from "./core/security-executor";
import {
  AuthoritySecurity,
  recordToSecurityEvent,
  type SecurityStorage,
} from "./authority-security";
import {
  MAX_CLOCK_SKEW,
  equal,
  Purpose,
  SecurityError,
  type Member,
} from "./device-security";
import {
  AuthService,
  ClientService,
  PairingService,
  DeviceKind,
  DeviceRegistrationSchema,
  ForwardResponseSchema,
  PutDeviceMetaRequestSchema,
  ListMachinesResponseSchema,
  ListRemoteSessionsResponseSchema,
  NotifyKind,
  PeerFrameSchema,
  ServerFrameSchema,
  StoredEventSchema,
  TermHelloSchema,
  PairApprovalSchema,
  RegisterDeviceRequestSchema,
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
  deviceID,
  newRequestID,
  signLogin,
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
  SessionStatus,
} from "./gen/remoteagent/v1/event_pb";
import { matchesFormat, mimeOf } from "./imageview";
import {
  ImageUploadState,
  DaemonSettingsSchema,
  ListModelsResponseSchema,
  type DaemonSettings,
  type ListModelsResponse,
  type Session,
} from "./gen/remoteagent/v1/local_pb";
import {
  CommandResultSchema,
  CommandSchema,
  DeviceMetaSchema,
  ImagePayloadSchema,
  ProjectPrefsSchema,
  TerminalFrameSchema,
  type CommandResult,
  type DeviceMeta,
  type MachineMeta,
  type ProjectPrefs,
  type TerminalFrame,
} from "./gen/remoteagent/v1/remote_pb";
import { describeBrowser, type DeviceInfo } from "./devices";
import type { Prepare, Tray } from "./attach";
import { modelLabel } from "./model";
import { CACHE_PAGE_SIZE, eventCache, type CachedEvent, type EventCache } from "./eventcache";
import { PEER_PROTOCOL_VERSION, RELAY_PROTOCOL_VERSION } from "./protocol";
import {
  loadAccount,
  saveAccount,
  saveAccountPublicProfile,
  loadRegistration,
  saveRegistration,
  clearRegistration,
  type StoredAccount,
} from "./storage";

export type CommandKind = MessageInitShape<typeof CommandSchema>["kind"];

export interface MachineView { id: string; online: boolean; lastSeenMs: bigint; meta?: MachineMeta; metaRev: bigint; keptOnline?: boolean; }
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

export const restoreSlot = (saved: Pick<RequestSlot, "requestId" | "deadline" | "prepared">, prior: boolean) =>
  new RequestSlot(saved.requestId, saved.deadline, saved.prepared, prior);
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

export function stillUnknown(error: unknown, wasUnknown: boolean): boolean {
  if (!(error instanceof CommandError)) return wasUnknown || uncertain(error);
  if (error.fromDaemon && error.outcome !== "not_sent" && !uncertain(error)) return false;
  return error.outcome === "not_sent" ? wasUnknown : (error.outcome === "unknown" || wasUnknown);
}

export class Attempt<P> {
  private pending?: { payload: P; slot: RequestSlot; tries: number; expired: boolean; frozen: boolean };
  get expired() { return !!this.pending?.expired; }
  get frozen() { return !!this.pending?.frozen; }
  get payload(): P | undefined { return this.pending?.payload; }
  next(payload: P): { payload: P; slot: RequestSlot } {
    this.pending ??= { payload, slot: newSlot(), tries: 0, expired: false, frozen: false };
    this.pending.tries++;
    if (Date.now() >= this.pending.slot.deadline && !this.pending.slot.evidence.unresolved) {
      this.pending = { payload, slot: newSlot(), tries: 1, expired: false, frozen: false };
    }
    return { payload: this.pending.payload, slot: this.pending.slot };
  }
  settle(error?: unknown, sent?: { slot: RequestSlot }) {
    const p = this.pending;
    if (!p || (sent && sent.slot !== p.slot)) return;
    const final = error instanceof CommandError && error.fromDaemon && error.outcome !== "not_sent" && !uncertain(error);
    if (error !== undefined && (stillUnknown(error, p.tries > 1 || p.frozen || !!p.slot.evidence.unresolved) || (!!p.slot.evidence.unresolved && !final))) {
      p.frozen = true;
      p.slot.evidence.includePrior(true);
      if (Date.now() >= p.slot.deadline + RESULT_RETENTION_MS) p.expired = true;
      return;
    }
    this.pending = undefined;
  }
  abandon() { this.pending = undefined; }
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

export function isInteractiveCommand(cmd: CommandKind): boolean {
  return ["send", "interrupt", "resolveApproval", "answerQuestion", "answerPlan", "cancelInput", "resumeInput"].includes(cmd?.case ?? "");
}

/** Largest piece of an image upload: each is sealed into one command, which must stay under 1 MiB. */
const UPLOAD_CHUNK = 768 * 1024;
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
export interface PrefsState { status: "loading" | "ready" | "error"; error: string; snap?: { revision: bigint; prefs: ProjectPrefs }; pending: Map<string, string>; failed?: { op: { id: string; kind: string; path: string }; error: string }; changedAt?: number; }
const emptyPrefs = () => create(ProjectPrefsSchema, { schema: 1 });

export async function joinWithApproval(
  accountPublicKey: Uint8Array<ArrayBuffer>,
  server: string,
  enrollment: { device: CryptoKeyPair; pairingId: string; approval: Uint8Array; approvalSignature: Uint8Array },
  existing?: StoredAccount,
  security?: SecurityRecord,
  replace = false,
): Promise<StoredAccount> {
  server = checkServer(server);
  const account = await deviceID(accountPublicKey);
  const device = enrollment.device;
  const pub = new Uint8Array(await crypto.subtle.exportKey("raw", device.publicKey));
  const a = fromBinary(PairApprovalSchema, enrollment.approval);
  const same = (x: Uint8Array, y: Uint8Array) => x.length === y.length && x.every((v, i) => v === y[i]);
  if (
    a.accountId !== account || a.pairingId !== enrollment.pairingId || a.candidateKind !== DeviceKind.CLIENT || a.replacesDeviceId !== "" ||
    !same(a.candidateDevicePublicKey, pub) || a.nonce.length !== 16 || a.expiresMs <= BigInt(Date.now()) ||
    a.approverPublicKey.length !== 32 || (await deviceID(a.approverPublicKey as Uint8Array<ArrayBuffer>)) !== a.approverDeviceId ||
    a.approverDeviceId === (await deviceID(pub))
  ) {
    throw new Error(l10n.t("security.pairing_approval_does_not_match_this_device_or_has"));
  }
  const signer = await crypto.subtle.importKey("raw", a.approverPublicKey as Uint8Array<ArrayBuffer>, "Ed25519", false, ["verify"]);
  if (!(await crypto.subtle.verify("Ed25519", signer, enrollment.approvalSignature as Uint8Array<ArrayBuffer>, concat(new TextEncoder().encode("remote-agent/v1/pair-approval\0"), enrollment.approval)))) {
    throw new Error(l10n.t("errors.invalid_pairing_approval_signature"));
  }
  const reg = toBinary(DeviceRegistrationSchema, create(DeviceRegistrationSchema, { accountPublicKey, devicePublicKey: pub, kind: DeviceKind.CLIENT, createdMs: BigInt(Date.now()) }));
  const stored: StoredAccount = { server, root: existing?.root, rootRaw: existing?.rootRaw && new Uint8Array(existing.rootRaw), devicePrivate: device.privateKey, deviceId: await deviceID(pub) };
  const request = toBinary(RegisterDeviceRequestSchema, create(RegisterDeviceRequestSchema, { protocolVersion: RELAY_PROTOCOL_VERSION, registration: reg, approval: enrollment.approval, approvalSignature: enrollment.approvalSignature }));
  await saveRegistration({ account: stored, request, security, replace });
  await resumeRegistration();
  return stored;
}

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

export type AuthorityBridge = Pick<
  AuthorityClient,
  | "lifecycle"
  | "account"
  | "security"
  | "isAuthority"
  | "pending"
  | "hasAccountPublicProfile"
  | "needsRecoveryCodeRotation"
  | "resumeSecurityOperation"
  | "renewLeaseNow"
  | "renewLeaseIfNeeded"
  | "removeDevice"
  | "deleteMachineRecords"
  | "prepareRecovery"
  | "prepareHandoffCode"
  | "publishRecovery"
  | "offerHandoff"
  | "discardUnpublishedCode"
  | "discardUnsignedStateDraft"
  | "handoffAuthority"
  | "hasMachineArchive"
  | "approvePairing"
>;

export class Remote implements CoreExecutor {
  private hostBridge?: HostBridge;
  private syncStateView?: SyncStateView;

  private persistedMachines = new Map<string, RemoteMachine>();
  private persistedSessions = new Map<string, RemoteSession>();
  private persistedOpen = new Set<string>();

  private trustedMembers = new Map<string, Member>();
  private archiveMachines = new Set<string>();
  private watchedSessionIds: string[] = [];
  private recoveryObjectsPending = false;
  private descriptionPending = false;
  private needsDescribeSelf = false;
  private securityPushSeq = 1n;
  private devicesPushSeq = 1n;
  private reconcilingSecurity = 0;
  private coreSyncDepth = 0;
  private securityRecordLoaded = false;
  private securityRecordLoading?: Promise<void>;
  private lastDispatchedRecord?: SecurityRecord;
  private devicesSyncWaiters: Array<() => void> = [];
  private serverAhead = false;
  authorityDeviceId = "";
  clockWarning = "";
  authorityError = "";

  readonly security: AuthoritySecurity;
  readonly authority: AuthorityBridge;
  private authorityClientPromise?: Promise<AuthorityClient>;
  directExec: DirectExecutor;
  private readonly secExec: WebSecurityExecutor;
  readonly deviceLink: DeviceLink;
  readonly startPairing: (web: string, replacesDeviceId?: string) => Promise<AuthorityPairing>;

  protected api: Client<typeof ClientService>;
  private unwatchDirectInstall?: () => void;
  private unwatchSecurityPush?: () => void;
  private securityTimer?: ReturnType<typeof setInterval>;
  private foreground = true;
  private cachedSyncMachines = new Map<string, SyncMachineView>();
  private latestXPrivate?: CryptoKey;
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
  drafts = new DraftTexts((key) => this.persistDraft(key));
  readonly approvalReasons = new Map<string, string>();
  readonly trays = new Map<string, Tray>();
  private localDrafts?: ComposerDraftStore;
  private readonly forgottenSessions = new Set<string>();
  private readonly draftLoaded = new Set<string>();
  private readonly draftLoads = new Map<string, Promise<void>>();
  private readonly draftRevisions = new Map<string, number>();
  private readonly localNotices = new Map<string, string>();
  readonly notices = new Map<string, string>();

  private cachedMachineMap = new Map<string, MachineView>();
  private cachedSessionMap = new Map<string, SessionView>();
  private cachedDeviceMap = new Map<string, DeviceInfo>();
  private cachedPrefPending = new Map<string, string>();
  private cachedInfoWarmed = false;
  private imageCache = new Map<string, ImageResult>();
  private models = new Map<string, Promise<ListModelsResponse>>();
  private modelReads = new Set<string>();
  private modelLists = new Map<string, ListModelsResponse>();
  private settingsCache = new Map<string, Promise<DaemonSettings | undefined>>();
  private settingsRead = new Map<string, DaemonSettings | undefined>();

  protected constructor(
    readonly account: StoredAccount,
    private cache?: EventCache,
    securityStorage?: SecurityStorage,
    protected readonly historyPageSize = 500,
    protected readonly sessionPageSize = SESSION_PAGE,
  ) {
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
        fetch: relayFetch,
        baseUrl: apiBase(account.server),
        useBinaryFormat: true,
        defaultTimeoutMs: RPC_TIMEOUT_MS,
        interceptors: [auth],
      }),
    );
    this.security = new AuthoritySecurity(
      account.deviceId,
      account.devicePrivate,
      async (after, limit, signal) => {
        const request = { afterEpoch: after, limit };
        const options = { signal, timeoutMs: this.everOnline ? 3000 : RPC_TIMEOUT_MS };
        const used = this.token;
        let reply;
        try {
          reply = await this.api.getSecurityStates(request, options);
        } catch (error) {
          signal?.throwIfAborted();
          if (!(error instanceof ConnectError) || error.code !== Code.Unauthenticated) throw error;
          if (!this.token || this.token === used) await this.login(signal);
          reply = await this.api.getSecurityStates(request, options);
        }
        return {
          states: reply.states.map((s) => ({ state: s.state, recovery: s.recovery })),
          headEpoch: reply.headEpoch,
          lease: reply.lease,
          recoveryPending: reply.recoveryPending,
          recoveryReadyMs: reply.recoveryReadyMs,
        };
      },
      () => !this.closed || this.reconcilingSecurity > 0,
      securityStorage,
      () => this.roundBudget(),
    );
    this.secExec = new WebSecurityExecutor({
      devicePrivate: () => this.account.devicePrivate,
      xPrivate: () => this.latestXPrivate,
      emit: (ev) => this.hostBridge?.dispatchEvent(ev),
    });
    this.deviceLink = new DeviceLink(
      new RelayPath(
        (id) => {
          const m = this.machines.get(id);
          const sm = this.cachedSyncMachines.get(id);
          const direct = !!sm?.directReady;
          return {
            relayRequired: !direct,
            unlinked: this.unlinked,
            reachable: direct || !!m?.online,
            ready: direct || this.conn === "online",
            authorized: !this.removed && !(this.archiveMachines.has(id) && !this.trustedMembers.has(id)),
            protocolVersion: direct ? PEER_PROTOCOL_VERSION : (sm?.protocolVersion || m?.meta?.protocolVersion),
            capabilities: sm?.meta?.capabilities ?? m?.meta?.capabilities,
          };
        },
        () => this.relayEpoch(),
        (request, options) => this.api.forward(request, options),
        (signal) => this.login(signal),
      ),
    );
    this.directExec = this.newDirectExecutor();
    this.security.onSync = (record, _started) => {
      void this.pushSecurityRecord(record);
    };
    this.security.onSyncFailed = () => {};
    this.security.onInstall = (record) => {
      void this.pushSecurityRecord(record);
    };
    const pairing = createClient(
      PairingService,
      createConnectTransport({
        fetch: relayFetch,
        baseUrl: apiBase(account.server),
        useBinaryFormat: true,
        defaultTimeoutMs: RPC_TIMEOUT_MS,
        interceptors: [auth],
      }),
    );
    const lifecycle = new AuthorityLifecycle();
    const loadAuthority = (): Promise<AuthorityClient> => {
      return (this.authorityClientPromise ??= import("./authority-client").then(
        ({ AuthorityClient }) =>
          new AuthorityClient(
            account,
            this.security,
            this.api,
            pairing,
            (publicKey, check, persist, request) => this.publishHandoffOffer(publicKey, check, persist, request),
            lifecycle,
            () => void this.watchPairingDelivery(),
          ),
      ));
    };
    this.authority = {
      lifecycle,
      account,
      security: this.security,
      pending: () => readSecurityOperation(),
      isAuthority: async () => (await loadAuthority()).isAuthority(),
      hasAccountPublicProfile: async () => (await loadAuthority()).hasAccountPublicProfile(),
      needsRecoveryCodeRotation: async () => (await loadAuthority()).needsRecoveryCodeRotation(),
      resumeSecurityOperation: async () => (await loadAuthority()).resumeSecurityOperation(),
      renewLeaseNow: async () => (await loadAuthority()).renewLeaseNow(),
      renewLeaseIfNeeded: async () => (await loadAuthority()).renewLeaseIfNeeded(),
      removeDevice: async (id) => (await loadAuthority()).removeDevice(id),
      deleteMachineRecords: async (id) => (await loadAuthority()).deleteMachineRecords(id),
      prepareRecovery: async () => (await loadAuthority()).prepareRecovery(),
      prepareHandoffCode: async () => (await loadAuthority()).prepareHandoffCode(),
      publishRecovery: async () => (await loadAuthority()).publishRecovery(),
      offerHandoff: async () => (await loadAuthority()).offerHandoff(),
      discardUnpublishedCode: async () => (await loadAuthority()).discardUnpublishedCode(),
      discardUnsignedStateDraft: async () => (await loadAuthority()).discardUnsignedStateDraft(),
      handoffAuthority: async (incoming, verifiedOffers) => (await loadAuthority()).handoffAuthority(incoming, verifiedOffers),
      hasMachineArchive: async (id) => (await loadAuthority()).hasMachineArchive(id),
      approvePairing: async (pair) => (await loadAuthority()).approvePairing(pair),
    };
    this.startPairing = async (web, replacesDeviceId = "") =>
      (await import("./authority-pairing")).AuthorityPairing.start(this.authority, pairing, web, replacesDeviceId);
  }

  static async load(): Promise<Remote | undefined> {
    await resumeRegistration();
    const acct = await loadAccount();
    if (!acct) return undefined;
    const r = new Remote(acct, eventCache());
    r.localDrafts = new ComposerDraftStore(acct);
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
      devicePrivate: () => this.account.devicePrivate,
      xPrivate: () => this.latestXPrivate,
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
    this.unwatchDirectInstall?.();
    this.unwatchDirectInstall = watchSecurityInstall((record) => {
      void this.pushSecurityRecord(record);
    });
    this.unwatchSecurityPush?.();
    this.unwatchSecurityPush = watchSecurityPush(() => {
      if (!this.closed) void this.security.sync(undefined, 5000, true).catch(() => undefined);
    });
    clearInterval(this.securityTimer);
    this.securityTimer = setInterval(() => {
      if (!this.closed && this.foreground && this.conn === "online") {
        void this.security.sync(undefined, 5000).catch(() => undefined);
      }
    }, 12_000);
    this.authority.lifecycle.resume();
    void this.watchPairingDelivery();
    this.ensureHost().start();
  }

  stop() {
    this.closed = true;
    this.directExec.close();
    this.secExec.close();
    while (this.devicesSyncWaiters.length) this.devicesSyncWaiters.shift()!();
    this.unwatchDirectInstall?.();
    this.unwatchDirectInstall = undefined;
    this.unwatchSecurityPush?.();
    this.unwatchSecurityPush = undefined;
    clearInterval(this.securityTimer);
    this.securityTimer = undefined;
    this.stopPairingDelivery();
    this.deviceLink.closeStreams(l10n.t("errors.terminal_connection_closed"));
    this.retireNetworkIO();
    for (const legId of [...this.legs.keys()]) this.closeStreamLeg(legId);
    cleanup(() => this.authority.lifecycle.pause());
    this.hostBridge?.stop();
  }

  dispose() {
    this.stop();
    disposeCoreHost(this);
    this.hostBridge = undefined;
  }

  private async watchPairingDelivery() {
    try {
      const pending = await this.authority.pending();
      if (this.closed || !pending?.delivery) {
        this.stopPairingDelivery();
        return;
      }
      this.pairingTimer ??= setInterval(() => void this.continuePairing(), 1500);
    } catch (e) {
      this.stopPairingDelivery();
      this.authorityError = errorText(e);
    }
  }

  private stopPairingDelivery() {
    clearInterval(this.pairingTimer);
    this.pairingTimer = undefined;
  }

  private async continuePairing() {
    if (this.closed || this.pairingBusy || (typeof document !== "undefined" && document.hidden)) return;
    this.pairingBusy = true;
    try {
      if (!(await this.authority.pending())?.delivery) {
        this.stopPairingDelivery();
        return;
      }
      await this.authority.resumeSecurityOperation();
      if (!(await this.authority.pending())?.delivery) this.stopPairingDelivery();
      this.authorityError = "";
      if (!this.closed) await this.loadDevices(false);
    } catch (e) {
      this.authorityError = errorText(e);
      if (
        e instanceof PairingDeliveryError ||
        (e instanceof SecurityError && ["invalid", "untrusted", "conflict", "storage", "capacity"].includes(e.code)) ||
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
    if (!this.syncStateView?.prefsSyncing && this.cachedPrefPending.size === 0) {
      return Promise.resolve(true);
    }
    return new Promise<boolean>((resolve) => {
      const unsub = this.subscribe(() => {
        if (!this.syncStateView?.prefsSyncing && this.cachedPrefPending.size === 0) {
          unsub();
          resolve(true);
        }
      });
    });
  }

  // --- SyncStateView projection ---

  applySyncStateView(view: SyncStateView) {
    const wasOnline = this.conn === "online";
    this.syncStateView = view;
    if (view.authorityDeviceId) this.authorityDeviceId = view.authorityDeviceId;
    this.clockWarning = view.accountClockSkewed ? l10n.t("errors.this_device_s_clock_is_behind_the_account_state") : "";

    const nextMachines = new Map<string, MachineView>();
    for (const pm of view.machines) {
      const authorized = !(this.archiveMachines.has(pm.machineId) && !this.trustedMembers.has(pm.machineId));
      nextMachines.set(pm.machineId, { id: pm.machineId, online: authorized && pm.online, lastSeenMs: pm.lastSeenMs, meta: pm.meta, metaRev: pm.metaRev, keptOnline: !view.machinesListed ? authorized && pm.online : undefined });
    }
    this.cachedMachineMap = nextMachines;
    this.cachedSyncMachines = new Map(view.machines.map((pm) => [pm.machineId, pm]));

    const nextSessions = new Map<string, SessionView>();
    for (const sv of view.sessions) {
      nextSessions.set(sv.sessionId, syncSessionToView(sv));
    }
    this.cachedSessionMap = nextSessions;

    const nextDevices = new Map<string, DeviceInfo>();
    for (const dv of view.devices) {
      if (view.securityLoaded && !dv.trusted) continue;
      nextDevices.set(dv.deviceId, { id: dv.deviceId, kind: dv.kind, pairingIncomplete: dv.createdMs === 0n && !dv.meta, createdMs: dv.createdMs, lastSeenMs: dv.lastSeenMs, meta: dv.meta, metaRev: dv.metaRev });
    }
    this.cachedDeviceMap = nextDevices;
    if (view.devices.length > 0 || view.machinesListed) this.devicesStatus = "ready";

    this.cachedPrefPending.clear();
    if (view.prefsSyncing) this.cachedPrefPending.set("", "syncing");
    while (this.devicesSyncWaiters.length) this.devicesSyncWaiters.shift()!();
    this.changed();
    if (!wasOnline && (this.conn as ConnState) === "online" && !this.closed) void restorePushPrefs(this);
  }

  get conn(): ConnState {
    if (this.removed) return "offline";
    return this.syncStateView?.connection === CoreConnState.CONNECTED ? "online" : this.syncStateView?.connection === CoreConnState.OFFLINE ? "offline" : "connecting";
  }
  get error(): string { return renderText(this.syncStateView?.connectionReason); }
  get connError(): string { return renderText(this.syncStateView?.connectionReason); }
  get failures(): number { return this.syncStateView?.connectionReason ? 1 : 0; }
  get connStep(): string { return renderText(this.syncStateView?.connectionStep); }
  get connStartedAt(): number | undefined { return undefined; }
  get everOnline(): boolean { return this.syncStateView?.liveLoaded ?? false; }
  get removed(): boolean { return Boolean(this.syncStateView?.removed || this.removalVerified); }
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
  get awaiting(): boolean { return (this.syncStateView?.connection ?? CoreConnState.CONNECTING) === CoreConnState.CONNECTING; }
  get unlinked(): boolean { return !this.reachabilityUnknown && (this.link === "no-network" || this.link === "no-server" || (this.link === "waiting" && !this.listed)); }
  get lostAt(): number | undefined { const ms = Number(this.syncStateView?.disconnectedAtMs ?? 0n); return ms > 0 ? ms : undefined; }
  get listed(): boolean { return !!(this.syncStateView?.machinesListed && this.syncStateView?.sessionsListed); }
  get listError(): string { return renderText(this.syncStateView?.sessionListError); }
  get moreSessions(): boolean { return this.syncStateView?.hasMoreSessions ?? false; }
  get sessionsOpen(): boolean { return this.moreSessions || !this.listed; }
  get loadingSessions(): boolean { return this.syncStateView?.loadingMoreSessions ?? false; }
  get machines(): ReadonlyMap<string, MachineView> { return this.cachedMachineMap; }
  get presence(): ReadonlyMap<string, MachineView> {
    if (!this.listed) return new Map<string, MachineView>();
    let projected: Map<string, MachineView> | undefined;
    for (const [id, machine] of this.cachedMachineMap) {
      const online = this.peer(id).reachable;
      if (online !== machine.online) { projected ??= new Map(this.cachedMachineMap); projected.set(id, { ...machine, online }); }
    }
    return projected ?? this.cachedMachineMap;
  }
  get sessions(): ReadonlyMap<string, SessionView> { return this.cachedSessionMap; }
  get devices(): ReadonlyMap<string, DeviceInfo> { return this.cachedDeviceMap; }
  deviceKind(id: string): DeviceKind | undefined {
    return this.cachedDeviceMap.get(id)?.kind ?? this.syncStateView?.devices.find((d) => d.deviceId === id)?.kind;
  }
  get prefs(): PrefsState {
    const v = this.syncStateView;
    return { status: (v?.prefsLoadFailed || v?.prefsBroken) && !v.prefs ? "error" : v?.prefsLoaded ? "ready" : "loading", error: "", snap: v?.prefs ? { revision: v.prefsRevision, prefs: v.prefs } : undefined, pending: this.cachedPrefPending };
  }
  projectPrefs(): ProjectPrefs { return this.syncStateView?.prefs ?? emptyPrefs(); }
  get isAuthority() { return (this.authorityDeviceId || this.syncStateView?.authorityDeviceId) === this.account.deviceId; }
  get recoveryPending() { return this.syncStateView?.recoveryPending ?? this.security.recoveryPending; }
  get recoveryReadyMs() { return this.syncStateView?.recoveryReadyMs || this.security.recoveryReadyMs; }
  get recoveryStatusStale() { return this.security.recoveryStatusStale; }
  demoFlag(_name: string): boolean { return false; }
  peer(machineId: string | undefined) { return this.deviceLink.status(machineId ?? ""); }
  onlineOf(m: MachineView): boolean { return this.listed ? this.peer(m.id).reachable : (m.keptOnline ?? this.peer(m.id).reachable); }
  pastPages(s: SessionView): boolean {
    const cursor = this.syncStateView?.nextCursor;
    return !!(this.moreSessions && cursor && (s.endedMs < cursor.endedMs || (s.endedMs === cursor.endedMs && s.id > cursor.sessionId)));
  }

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

  securityConfig(): { account: string; deviceId: string } | undefined {
    return { account: "", deviceId: this.account.deviceId };
  }

  syncSecuritySnapshot?(opId: bigint, networkGeneration: bigint, touch: boolean): Promise<SecuritySnapshotEvent>;

  securityKeySign(eff: SecurityKeySignEffect) {
    void this.secExec.keySign(eff);
  }

  securityKeyAgree(eff: SecurityKeyAgreeEffect) {
    void this.secExec.keyAgree(eff);
  }

  securityCryptoBatch(eff: SecurityCryptoBatchEffect) {
    void this.secExec.cryptoBatch(eff);
  }

  private applySecurityRecordSets(record: SecurityRecord) {
    const head = record.states[record.states.length - 1];
    this.latestXPrivate = record.xPrivate;
    this.authorityDeviceId = head.core.authorityID;
    this.trustedMembers = new Map(head.core.members.map((m) => [m.id, m]));
    this.archiveMachines = new Set(record.states.flatMap((s) => s.core.members.filter((m) => m.kind === 1).map((m) => m.id)));
  }

  private async toSecurityRecordEvent(
    record: SecurityRecord,
    opts: { opId?: bigint; pushSeq?: bigint; networkGeneration?: bigint; error?: string } = {},
  ): Promise<SecurityRecordEvent> {
    this.applySecurityRecordSets(record);
    return recordToSecurityEvent(record, {
      opId: opts.opId ?? 0n,
      pushSeq: opts.pushSeq ?? 0n,
      networkGeneration: opts.networkGeneration ?? BigInt(this.networkGeneration),
      recoveryPending: this.security.recoveryPending,
      recoveryReadyMs: this.security.recoveryReadyMs,
      recoveryObjectsPending: this.recoveryObjectsPending,
      error: opts.error,
    });
  }

  async readCachedSecurityRecord(): Promise<SecurityRecordEvent | undefined> {
    const rec = await this.security.verifiedRecord();
    const ev = await this.toSecurityRecordEvent(rec);
    if (this.hostBridge) {
      this.securityRecordLoaded = true;
      this.lastDispatchedRecord = rec;
    }
    return ev;
  }

  private async ensureSecurityRecordLoaded(knownRecord?: SecurityRecord): Promise<void> {
    if (knownRecord) this.applySecurityRecordSets(knownRecord);
    const prev = this.securityRecordLoading ?? Promise.resolve();
    const job = prev
      .catch(() => undefined)
      .then(async () => {
        const rec = knownRecord ?? (await this.security.verifiedRecord());
        if (this.hostBridge && this.securityRecordLoaded && this.lastDispatchedRecord === rec) {
          return;
        }
        const pushSeq = this.securityPushSeq++;
        const ev = await this.toSecurityRecordEvent(rec, { pushSeq });
        if (!this.closed) {
          this.ensureHost().dispatchEvent(create(CoreEventSchema, { event: { case: "securityRecord", value: ev } }));
          this.securityRecordLoaded = true;
          this.lastDispatchedRecord = rec;
        }
      })
      .finally(() => {
        if (this.securityRecordLoading === job) {
          this.securityRecordLoading = undefined;
        }
      });
    this.securityRecordLoading = job;
    return job;
  }

  private async pushSecurityRecord(record: SecurityRecord): Promise<void> {
    this.applySecurityRecordSets(record);
    if (this.coreSyncDepth === 0 && !this.closed && this.hostBridge) {
      const pushSeq = this.securityPushSeq++;
      try {
        const ev = await this.toSecurityRecordEvent(record, { pushSeq });
        if (!this.closed && this.hostBridge) {
          this.hostBridge.dispatchEvent(create(CoreEventSchema, { event: { case: "securityRecord", value: ev } }));
          this.securityRecordLoaded = true;
          this.lastDispatchedRecord = record;
        }
      } catch {
        // Record serialization failure will be retried on next sync.
      }
    }
  }

  async syncSecurity(opId: bigint, networkGeneration: bigint, touch: boolean): Promise<SecurityRecordEvent> {
    const pushSeq = opId === 0n ? this.securityPushSeq++ : 0n;
    const gen = Number(networkGeneration);
    if (gen > this.networkGeneration) { this.networkGeneration = gen; this.retireNetworkIO(); }
    if (touch) this.touchAfterWake();
    this.coreSyncDepth++;
    try {
      const record = await this.security.sync(undefined, 5000, true);
      this.verifiedNetworkGeneration = this.networkGeneration;
      this.wakeRequired = false;
      this.maintainAuthority();
      if (this.recoveryObjectsPending) {
        const preEv = await this.toSecurityRecordEvent(record, { opId: 0n, pushSeq: 0n, networkGeneration });
        if (!this.closed) {
          this.ensureHost().dispatchEvent(create(CoreEventSchema, { event: { case: "securityRecord", value: preEv } }));
          this.securityRecordLoaded = true;
          this.lastDispatchedRecord = record;
        }
        try { await this.republishRecoveryObjects(record, this.api); this.recoveryObjectsPending = false; }
        catch { /* Retry on subsequent security sync. */ }
      }
      if (this.needsDescribeSelf) await this.describeSelf();
      if (record.member === false) { this.removalVerified = true; this.discardToken(); await this.tokenWrites; }
      const ev = await this.toSecurityRecordEvent(record, { opId, pushSeq, networkGeneration });
      if (this.hostBridge) {
        this.securityRecordLoaded = true;
        this.lastDispatchedRecord = record;
      }
      return ev;
    } catch (e) {
      return create(SecurityRecordEventSchema, {
        opId,
        pushSeq,
        networkGeneration,
        recoveryPending: this.security.recoveryPending,
        recoveryReadyMs: this.security.recoveryReadyMs,
        recoveryObjectsPending: this.recoveryObjectsPending,
        error: errorText(e),
      });
    } finally { this.coreSyncDepth--; }
  }

  async readBootstrapCache(opId: bigint): Promise<CacheBootstrapLoadedEvent> {
    await this.warmInfoOnce();
    const homeScopeToken = readStoredScopeToken();
    let scopeHmacKey = new Uint8Array(32);
    const wireMachines: RemoteMachine[] = [], wireSessions: RemoteSession[] = [];
    let wirePrefs: AccountPrefsObject | undefined;
    if (!this.cache) { crypto.getRandomValues(scopeHmacKey); return create(CacheBootstrapLoadedEventSchema, { opId, homeScopeToken, scopeHmacKey }); }
    try {
      const existingKey = await this.cache.get(SCOPE_HMAC_KEY_VALUE);
      if (existingKey instanceof Uint8Array && existingKey.length === 32) scopeHmacKey = new Uint8Array(existingKey);
      else { crypto.getRandomValues(scopeHmacKey); await this.cache.set(SCOPE_HMAC_KEY_VALUE, scopeHmacKey); }
      const rec = await this.security.verifiedRecord();
      await this.ensureSecurityRecordLoaded(rec);
      for (const id of this.archiveMachines) if (!this.trustedMembers.has(id)) void this.keepValue("schedules:" + id, undefined);

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
      const knownLast = this.sessions.get(sessionId)?.lastSeq;
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
        this.forgetLocalSession(eff.kind.value);
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
          await this.ensureSecurityRecordLoaded();
          await this.maybeRepublishAndDescribe(dRes.devices);
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
          await this.ensureSecurityRecordLoaded();
          await this.maybeRepublishAndDescribe(res.devices);
          return this.rpcEv({ case: "devicesFetched", value: create(DevicesFetchedEventSchema, { opId, wireDevices: res.devices }) });
        } catch (e) {
          return this.rpcEv({ case: "devicesFetched", value: create(DevicesFetchedEventSchema, { opId, error: errorText(e) }) });
        }
      default:
        throw new Error("unexpected rpc effect");
    }
  }

  private async maybeRepublishAndDescribe(devices: Device[]) {
    try {
      const record = await this.security.verifiedRecord();
      this.recoveryObjectsPending = false;
      try {
        await this.republishRecoveryObjects(record, this.api);
      } catch {
        this.recoveryObjectsPending = true;
      }
      const me = devices.find((d) => d.id === this.account.deviceId);
      if (me && (!me.meta || !me.meta.securityHeader.length)) {
        void this.describeSelf();
      }
    } catch {
      /* Verified record missing before initial security sync. */
    }
  }

  // --- CoreExecutor: Single-Attempt Command Dispatch (including Direct-First + Interactive Dual-Send) ---

  directKeySign(eff: DirectKeySignEffect) { void this.directExec.keySign(eff); }
  directKeyAgree(eff: DirectKeyAgreeEffect) { void this.directExec.keyAgree(eff); }
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
          !(this.archiveMachines.has(params.machineId) && !this.trustedMembers.has(params.machineId))
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
      const hashesMatch = !!requestKeyHash && !!payloadHash && equal(msg.requestKeyHash, requestKeyHash) && equal(msg.payloadHash, payloadHash) && (msg.outcome === "conflict" || msg.conflictKind === "");
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
        !(this.archiveMachines.has(params.machineId) && !this.trustedMembers.has(params.machineId))
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
    await this.ensureSecurityRecordLoaded();
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
    await this.security.sync();
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
    await this.ensureSecurityRecordLoaded();
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
  private touchAfterWake() { this.wakeRequired = true; this.wakeRevision++; }
  private confirmWake(generation: number, wake: number) { if (generation === this.networkGeneration && wake === this.wakeRevision) this.wakeRequired = false; }

  private async ensureWakeSecurity() {
    if (!this.wakeRequired) return;
    if (!this.wakeSecurity) {
      const generation = this.networkGeneration, wake = this.wakeRevision, controller = new AbortController();
      this.wakeController = controller;
      const pending = ownedRound(controller, this.roundBudget(), async () => {
        await this.security.sync(controller.signal);
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
        const res = await auth.login({ protocolVersion: RELAY_PROTOCOL_VERSION, deviceId: this.account.deviceId, challenge: ch.challenge, signature: await signLogin(this.account.devicePrivate, ch.challenge) }, { signal, timeoutMs });
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

  private maintainAuthority() {
    if (typeof navigator === "undefined" || !navigator.locks) return;
    void (async () => {
      try {
        if (!this.isAuthority && !(await readSecurityOperation())) return;
        await this.authority.renewLeaseIfNeeded();
        this.authorityError = "";
      } catch (e) { this.authorityError = errorText(e); }
    })();
  }

  private noteClock(e: unknown) { if (e instanceof SecurityError && e.code === "clock") this.clockWarning = l10n.t("errors.this_device_s_clock_is_behind_the_account_state"); }

  private keepValue(name: string, value: Record<string, unknown> | undefined) {
    return this.cache?.set(name, value && { ...value, deviceId: this.account.deviceId, server: this.account.server }).catch(() => undefined);
  }

  private async warmInfoOnce() {
    if (this.cachedInfoWarmed || !this.cache) return;
    this.cachedInfoWarmed = true;
    try {
      const info = (await this.cache.get("info")) as Record<string, unknown> | undefined;
      if (info && info.deviceId === this.account.deviceId && info.server === this.account.server) {
        for (const [k, b] of Object.entries((info.models ?? {}) as Record<string, Uint8Array>)) if (!this.modelLists.has(k)) this.modelLists.set(k, fromBinary(ListModelsResponseSchema, b));
        for (const [k, b] of Object.entries((info.settings ?? {}) as Record<string, Uint8Array>)) if (!this.settingsRead.has(k)) this.settingsRead.set(k, fromBinary(DaemonSettingsSchema, b));
      }
    } catch { /* ignore cached info read failure */ }
  }

  private keepInfo() {
    if (!this.cache) return;
    const models: Record<string, Uint8Array> = {}, settings: Record<string, Uint8Array> = {};
    for (const [k, v] of this.modelLists) models[k] = toBinary(ListModelsResponseSchema, v);
    for (const [k, v] of this.settingsRead) if (v) settings[k] = toBinary(DaemonSettingsSchema, v);
    void this.keepValue("info", { models, settings });
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
    const s = this.sessions.get(sid);
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
    const m = this.machines.get(machineId);
    const theirs = this.peer(machineId).protocolVersion;
    if (theirs !== undefined && theirs !== PEER_PROTOCOL_VERSION) {
      throw new CommandError("failed_precondition", theirs < PEER_PROTOCOL_VERSION ? l10n.t("errors.remote_agent_on_is_out_of_date_update_it", { value: String(machineName(m)) }) : l10n.t("errors.this_page_is_out_of_date_refresh_or_update"), false, false, slot.evidence.unresolved ? "unknown" : "not_sent");
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

  // --- Shared Files / Schedules / Drafts / Outbox / Answers / Images / Terminal / Models / Settings / Devices ---

  sharedFilesHost() {
    return {
      getChunk: (sid: string, fileId: string, offset: bigint) => this.getSharedFileChunk(sid, fileId, offset),
      isConnected: (sid: string) => this.peer(this.sessions.get(sid)?.machineId).ready,
      isMachineOnline: (sid: string) => {
        const s = this.sessions.get(sid);
        return !!(s && this.peer(s.machineId).reachable);
      },
      isSessionEnded: (sid: string) => {
        const s = this.sessions.get(sid);
        return s?.info?.status === SessionStatus.EXITED || (s?.endedMs ?? 0n) > 0n;
      },
      sharedFileChunkBytes: (sid: string) => {
        const s = this.sessions.get(sid);
        return (s && this.peer(s.machineId).capabilities?.sharedFileChunkBytes) || 0;
      },
    };
  }

  hasSharedFilesCapability(sid: string): boolean {
    const s = this.sessions.get(sid);
    return !!(s && this.peer(s.machineId).capabilities?.sharedFiles === true);
  }

  async readScheduleCache(opId: bigint, machineId: string): Promise<ScheduleCacheLoadedEvent> {
    if (!machineId || this.closed || (this.archiveMachines.has(machineId) && !this.trustedMembers.has(machineId))) {
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

  draft(key: string): string { return this.drafts.get(key) ?? ""; }
  setDraft(key: string, text: string) {
    text ? this.drafts.set(key, text) : this.drafts.delete(key);
    void this.persistDraft(key)?.catch(() => undefined);
    this.changed();
  }

  private clearLocalNotice(key: string) {
    const tray = this.trays.get(key);
    const notice = this.localNotices.get(key);
    if (tray && notice && tray.note === notice) {
      tray.note = "";
      this.touch();
    }
    this.localNotices.delete(key);
  }

  persistDraft(key: string): Promise<void> | undefined {
    const revision = (this.draftRevisions.get(key) ?? 0) + 1;
    this.draftRevisions.set(key, revision);
    const tray = this.trays.get(key);
    if (this.closed || this.forgottenSessions.has(key) || !this.localDrafts || !tray) return;
    if (!this.draftLoaded.has(key)) {
      if (!this.draftLoads.has(key)) void this.restoreDraft(key, tray).catch(() => undefined);
      return;
    }
    const text = this.drafts.get(key) ?? "";
    const snapshot = { items: [...tray.items], original: tray.original };
    const save = this.localDrafts.save(key, text, snapshot);
    void save.then(
      () => {
        if (this.draftRevisions.get(key) === revision) this.clearLocalNotice(key);
      },
      () => this.localNotice(key, l10n.t("errors.the_draft_could_not_be_saved_locally_keep_the")),
    );
    return save;
  }

  restoreDraft(key: string, tray: Tray): Promise<void> {
    if (!this.localDrafts || this.forgottenSessions.has(key) || this.draftLoaded.has(key)) {
      return Promise.resolve();
    }
    const existing = this.draftLoads.get(key);
    if (existing) return existing;
    tray.restoring = true;
    const revision = this.draftRevisions.get(key) ?? 0;
    const job = this.localDrafts
      .read(key)
      .then((saved) => {
        if (this.closed || this.trays.get(key) !== tray) return;
        if (saved) {
          if ((this.draftRevisions.get(key) ?? 0) === revision) this.drafts.restore(key, saved.text);
          tray.hydrate(saved.items, saved.original);
        }
        this.draftLoaded.add(key);
        this.clearLocalNotice(key);
      })
      .catch((error) => {
        this.draftLoads.delete(key);
        this.localNotice(key, l10n.t("errors.the_draft_could_not_be_restored_you_can_still"));
        throw error;
      })
      .finally(() => {
        tray.restoring = false;
        if (!this.closed && this.draftLoaded.has(key)) void this.persistDraft(key)?.catch(() => undefined);
        if (!this.closed) this.touch();
      });
    this.draftLoads.set(key, job);
    return job;
  }

  private forgetLocalSession(sid: string) {
    this.forgottenSessions.add(sid);
    this.drafts.delete(sid);
    this.draftLoaded.delete(sid);
    this.draftLoads.delete(sid);
    this.draftRevisions.delete(sid);
    this.localNotices.delete(sid);
    this.trays.delete(sid);
    this.scroll.delete(sid);
    void this.localDrafts?.removeSession(sid).catch(() => undefined);
  }

  private localNotice(key: string, message: string) {
    const tray = this.trays.get(key);
    if (!this.closed && tray) {
      this.localNotices.set(key, message);
      tray.note = message;
      this.touch();
    }
    this.notices.set(key, message);
    this.changed();
  }
  clearNotice(sid: string) {
    if (this.notices.delete(sid)) this.changed();
  }
  tray(key: string): Tray | undefined { return this.trays.get(key); }
  setTray(key: string, tray: Tray) { this.trays.set(key, tray); }

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

  prepareImage: Prepare = async (file, original) => (await import("./sendimages")).prepareForSending(file, original);

  async uploadImage(
    machineId: string,
    p: { bytes: Uint8Array; format: string; fileName?: string },
    progress: (done: number, total: number) => void = () => undefined,
    started: (abort: () => void) => void = () => undefined,
  ): Promise<{ imageId: string; fileId?: string; expiresMs: number }> {
    const format = { jpeg: ImageFormat.JPEG, png: ImageFormat.PNG, webp: ImageFormat.WEBP, gif: ImageFormat.GIF }[p.format] ?? ImageFormat.UNSPECIFIED;
    const transferId = newRequestID();
    const sha256 = new Uint8Array(await crypto.subtle.digest("SHA-256", p.bytes as Uint8Array<ArrayBuffer>));
    const total = p.bytes.length;
    const statusOf = (r: CommandResult) => {
      const k = r.kind;
      const st = k.case === "beginImageUpload" || k.case === "putImageChunk" || k.case === "getImageUploadStatus" || k.case === "completeImageUpload" ? k.value.status : undefined;
      if (!st) throw new Error(l10n.t("new_session.unexpected_reply"));
      return st;
    };
    const step = async (cmd: CommandKind) => {
      for (let i = 0; ; i++) {
        try {
          const st = statusOf(await this.machineCommand(machineId, cmd));
          if (cmd?.case === "completeImageUpload" && st.state === ImageUploadState.RECEIVING && Number(st.persistedBytes) === total) {
            if (i >= 2) throw new Error(l10n.t("errors.the_computer_is_still_verifying_attachments_retry_shortly"));
            continue;
          }
          return st;
        } catch (e) {
          if (!uncertain(e) || (e instanceof CommandError && e.outcome !== "unknown") || i >= 2) throw e;
          try {
            const st = statusOf(await this.machineCommand(machineId, { case: "getImageUploadStatus", value: { transferId } }));
            if (cmd?.case === "completeImageUpload" && st.state === ImageUploadState.RECEIVING && Number(st.persistedBytes) === total) continue;
            return st;
          } catch (e2) {
            if (!uncertain(e2) || (e2 instanceof CommandError && e2.outcome !== "unknown") || i >= 2) throw e2;
          }
        }
      }
    };
    let st = await step({ case: "beginImageUpload", value: { transferId, format, fileName: p.fileName ?? "", totalBytes: BigInt(total), sha256 } });
    started(() => void this.machineCommand(machineId, { case: "abortImageUpload", value: { transferId } }).catch(() => undefined));
    const chunk = Math.min(UPLOAD_CHUNK, st.maxChunkBytes > 0 ? st.maxChunkBytes : UPLOAD_CHUNK);
    for (let guard = 0; Number(st.persistedBytes) < total; guard++) {
      if (guard > total / chunk + 10) throw new Error(l10n.t("errors.upload_made_no_progress"));
      const offset = Number(st.persistedBytes);
      progress(offset, total);
      st = await step({ case: "putImageChunk", value: { transferId, offset: BigInt(offset), data: p.bytes.subarray(offset, offset + chunk) } });
    }
    progress(total, total);
    st = await step({ case: "completeImageUpload", value: { transferId } });
    if (p.fileName ? !st.fileId : !st.imageId) throw new Error(l10n.t("errors.the_computer_did_not_return_an_attachment_id"));
    return { imageId: st.imageId, ...(st.fileId ? { fileId: st.fileId } : {}), expiresMs: Number(st.expiresMs) };
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

  async getSharedFileChunk(sid: string, fileId: string, offset: bigint): Promise<SharedFileChunkResult> {
    const res = await this.command(sid, { case: "getSharedFileChunk", value: { sessionId: sid, fileId, offset } });
    if (res.kind.case !== "getSharedFileChunk") throw new Error(l10n.t("new_session.unexpected_reply"));
    return { data: res.kind.value.data, totalBytes: res.kind.value.totalBytes };
  }

  cachedImage(sid: string, imageId: string): ImageResult | undefined { return this.imageCache.get(sid + "/" + imageId); }
  authHeader(): string { return "Bearer " + this.token; }
  async pushKey(): Promise<string> { return (await this.api.getPushKey({})).publicKey; }
  async setPushSubscription(subscriptionJson: string, kinds: NotifyKind[]): Promise<void> {
    const capability = subscriptionJson ? await this.api.getPushKey({}) : undefined;
    const selected = subscriptionJson ? machineNotificationKinds(kinds, capability?.machineDiskNotifications === true) : [];
    await this.api.setPushSubscription({ subscriptionJson, kinds: selected });
  }

  modelName(machineId: string, agent: Agent, slug: string): string {
    return modelLabel(this.modelLists.get(machineId + "/" + agent)?.models, slug, agent);
  }
  cachedModels(machineId: string, agent: Agent): ListModelsResponse | undefined {
    return this.modelLists.get(machineId + "/" + agent);
  }
  listModels(machineId: string, agent: Agent = DEFAULT_AGENT, fresh = false): Promise<ListModelsResponse> {
    const key = machineId + "/" + agent;
    let p = fresh && !this.modelReads.has(key) ? undefined : this.models.get(key);
    if (!p) {
      p = this.machineCommand(machineId, { case: "listModels", value: { agent } }).then((r) => {
        if (r.kind.case !== "listModels") throw new Error(l10n.t("new_session.unexpected_reply"));
        this.modelLists.set(key, r.kind.value);
        this.keepInfo();
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

  cachedSettings(machineId: string): DaemonSettings | undefined { return this.settingsRead.get(machineId); }
  settings(machineId: string, fresh = false): Promise<DaemonSettings | undefined> {
    let p = fresh ? undefined : this.settingsCache.get(machineId);
    if (!p) {
      p = this.machineCommand(machineId, { case: "getSettings", value: {} }).then((r) => {
        if (r.kind.case !== "getSettings") throw new Error(l10n.t("new_session.unexpected_reply"));
        if (this.settingsCache.get(machineId) === p) {
          this.settingsRead.set(machineId, r.kind.value.settings);
          this.keepInfo();
          this.changed();
        }
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
      const record = await this.security.sync();
      await this.ensureSecurityRecordLoaded(record);
      const res = await this.api.listDevices({});
      await this.maybeRepublishAndDescribe(res.devices);
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
    } catch {
      this.devicesStatus = "error";
      this.changed();
    }
  }

  async handoffCandidates(): Promise<HandoffOffer[]> {
    await this.loadDevices(false);
    const { handoffCandidates: verifiedHandoffCandidates } = await import("./authority-client");
    return verifiedHandoffCandidates(
      await this.security.verifiedRecord(),
      [...this.devices.values()].flatMap((d) =>
        d.meta?.handoffRecoveryEd.length === 32 ? [{ id: d.id, recoveryEd: new Uint8Array(d.meta.handoffRecoveryEd) }] : [],
      ),
    );
  }

  async handoffAuthority(incoming: string) {
    await this.authority.handoffAuthority(incoming, await this.handoffCandidates());
    await this.loadDevices(false);
    this.hostBridge?.retryList();
  }

  private async checkLocalRemoval() {
    this.reconcilingSecurity++;
    try {
      if ((await this.security.verifiedRecord()).member === false) {
        this.removalVerified = true;
        this.discardToken();
        this.stop();
      }
    } catch {
      /* Keep unverified notice and completion material. */
    } finally {
      this.reconcilingSecurity--;
    }
  }

  async resumeSecurityOperation() {
    const stopped = this.closed;
    if (stopped) this.authority.lifecycle.resume();
    this.reconcilingSecurity++;
    try {
      await this.authority.resumeSecurityOperation();
      await this.checkLocalRemoval();
      if (!this.closed) await this.loadDevices(false);
      this.hostBridge?.retryList();
    } finally {
      this.reconcilingSecurity--;
      if (stopped || this.closed) this.authority.lifecycle.pause();
    }
  }

  async removeDevice(id: string) {
    await this.authority.removeDevice(id);
    await this.loadDevices(false);
    this.hostBridge?.retryList();
  }

  async deleteMachineRecords(id: string) {
    await this.authority.deleteMachineRecords(id);
    this.hostBridge?.retryList();
  }

  renameDevice(id: string, name: string): Promise<void> {
    if (id !== this.account.deviceId) throw new Error(l10n.t("errors.names_can_only_be_changed_on_that_device"));
    return this.changeDeviceMeta(id, (m) => ({
      name: name.trim(),
      platform: m?.platform ?? "",
      description: m?.description ?? "",
      handoffRecoveryEd: m?.handoffRecoveryEd,
    }));
  }

  private async describeSelf() {
    if (this.descriptionPending) return;
    this.descriptionPending = true;
    try {
      await this.changeDeviceMeta(this.account.deviceId, (m) => ({
        name: m?.name ?? "",
        platform: "web",
        description: m?.description || describeBrowser(navigator.userAgent),
        handoffRecoveryEd: m?.handoffRecoveryEd,
      }));
      this.needsDescribeSelf = false;
    } catch {
      this.needsDescribeSelf = true;
    } finally {
      this.descriptionPending = false;
    }
  }

  private async publishHandoffOffer(
    publicKey: Uint8Array<ArrayBuffer>,
    check: () => void,
    persist: (request: Uint8Array<ArrayBuffer>) => Promise<void>,
    signed?: Uint8Array<ArrayBuffer>,
  ) {
    let request;
    if (signed) request = fromBinary(PutDeviceMetaRequestSchema, signed);
    else {
      await this.loadDevices(false);
      check();
      if (this.devicesStatus !== "ready") throw new Error(l10n.t("errors.device_information_could_not_be_verified"));
      const own = this.devices.get(this.account.deviceId);
      const revision = own?.metaRev ?? 0n;
      const meta = (await this.sealSecureMessage(
        { kind: PayloadKind.DEVICE_META, requestId: this.account.deviceId, seq: revision + 1n, sender: this.account.deviceId },
        DeviceMetaSchema,
        create(DeviceMetaSchema, { ...create(DeviceMetaSchema, own?.meta), handoffRecoveryEd: publicKey }),
        check,
      )).envelope;
      request = create(PutDeviceMetaRequestSchema, { deviceId: this.account.deviceId, expectedRevision: revision, meta });
      check();
      await persist(toBinary(PutDeviceMetaRequestSchema, request));
      check();
    }
    if (request.deviceId !== this.account.deviceId) throw new SecurityError("untrusted");
    check();
    const result = await this.api.putDeviceMeta(request);
    check();
    if (!result.applied) {
      await this.loadDevices(false);
      check();
      const own = this.devices.get(this.account.deviceId);
      if (!own?.meta || own.metaRev <= request.expectedRevision || !equalBytes(own.meta.handoffRecoveryEd, publicKey)) return false;
    }
    return true;
  }

  private async changeDeviceMeta(
    id: string,
    change: (m: DeviceMeta | undefined) => Omit<Partial<DeviceMeta>, "$typeName">,
    check: () => void = () => undefined,
  ) {
    if (id !== this.account.deviceId) throw new Error(l10n.t("errors.names_can_only_be_changed_on_that_device"));
    for (let i = 0; i < 5; i++) {
      await this.loadDevices(false);
      if (this.devicesStatus !== "ready") throw new Error(l10n.t("errors.device_information_could_not_be_verified"));
      check();
      const cur = this.devices.get(id);
      const rev = cur?.metaRev ?? 0n;
      const env = (await this.sealSecureMessage(
        { kind: PayloadKind.DEVICE_META, requestId: id, seq: rev + 1n, sender: this.account.deviceId },
        DeviceMetaSchema,
        create(DeviceMetaSchema, change(cur?.meta)),
        check,
      )).envelope;
      check();
      const r = await this.api.putDeviceMeta({ deviceId: id, expectedRevision: rev, meta: env });
      check();
      if (r.applied) {
        await this.loadDevices(false);
        return;
      }
    }
    throw new Error(l10n.t("errors.device_information_is_changing_retry"));
  }

  private async republishRecoveryObjects(record: SecurityRecord, api = this.api) {
    const boundary = record.states.filter((_, i) => record.proofs[i]).at(-1)?.core.contentEpoch;
    if (!boundary) return;
    await this.ensureSecurityRecordLoaded(record);
    for (let attempt = 0; attempt < 5; attempt++) {
      const { object: o } = await api.getAccountPrefs({ objectId: PREFS_OBJECT });
      if (!o?.envelope || o.revision === 0n) break;
      let snapshot;
      try {
        snapshot = await this.openSecureMessage(
          o.envelope,
          { kind: PayloadKind.ACCOUNT_PREFS, requestId: PREFS_OBJECT, seq: o.revision },
          ProjectPrefsSchema,
          true,
        );
      } catch (e) {
        if (e instanceof SecurityError && e.code === "unavailable") break;
        throw e;
      }
      if (snapshot.contentEpoch >= boundary) break;
      const { envelope } = await this.sealSecureMessage(
        { kind: PayloadKind.ACCOUNT_PREFS, requestId: PREFS_OBJECT, seq: o.revision + 1n, sender: this.account.deviceId },
        ProjectPrefsSchema,
        snapshot.msg,
      );
      if ((await api.putAccountPrefs({ objectId: PREFS_OBJECT, expectedRevision: o.revision, envelope })).applied) break;
      if (attempt === 4) throw new Error(l10n.t("errors.the_recovery_object_was_updated_concurrently_retrying_on_the"));
    }
    for (let attempt = 0; attempt < 5; attempt++) {
      const own = (await api.listDevices({})).devices.find((d) => d.id === this.account.deviceId);
      if (!own?.meta) break;
      const snapshot = await this.openSecureMessage(
        own.meta,
        { kind: PayloadKind.DEVICE_META, requestId: own.id, seq: own.metaRevision },
        DeviceMetaSchema,
        true,
      );
      if (snapshot.header.sender !== own.id) {
        throw new SecurityError("untrusted");
      }
      if (snapshot.contentEpoch >= boundary) break;
      const { envelope: meta } = await this.sealSecureMessage(
        { kind: PayloadKind.DEVICE_META, requestId: own.id, seq: own.metaRevision + 1n, sender: this.account.deviceId },
        DeviceMetaSchema,
        snapshot.msg,
      );
      if ((await api.putDeviceMeta({ deviceId: own.id, expectedRevision: own.metaRevision, meta })).applied) break;
      if (attempt === 4) throw new Error(l10n.t("errors.the_recovery_object_was_updated_concurrently_retrying_on_the"));
    }
  }
}

export function errorText(e: unknown): string {
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

export async function resumeRegistration(): Promise<void> {
  const authority = await readSecurityOperation();
  if (authority?.kind === "create") throw new Error(l10n.t("errors.account_creation_draft_preserved_continue_the_original_creation_flow"));
  if (authority?.kind === "recovery") throw new Error(l10n.t("errors.emergency_recovery_draft_preserved_continue_checking_waiting_or_completing"));
  const pending = await loadRegistration();
  if (!pending) return;
  const request = fromBinary(RegisterDeviceRequestSchema, pending.request);
  if (pending.security) {
    const registration = fromBinary(DeviceRegistrationSchema, request.registration);
    await saveAccountPublicProfile(pending.account.server, pending.security.pin, new Uint8Array(registration.accountPublicKey));
    const r = pending.security;
    const { initializeSecurity } = await import("./device-security-store");
    await initializeSecurity(r.pin, r.states, r.proofs, r.memberID, r.xPrivate, pending.replace, r.lease);
  }
  await saveAccount(pending.account);
  localStorage.setItem("ra-server", pending.account.server);
  const auth = createClient(
    AuthService,
    createConnectTransport({ fetch: relayFetch, baseUrl: apiBase(pending.account.server), useBinaryFormat: true }),
  );
  const res = await auth.registerDevice(request, { timeoutMs: 15_000 });
  if (res.deviceId !== pending.account.deviceId) throw new Error(l10n.t("errors.the_server_returned_an_unexpected_device_id"));
  const approval = fromBinary(PairApprovalSchema, fromBinary(RegisterDeviceRequestSchema, pending.request).approval);
  const { clearPairingIdentity } = await import("./device-security-store");
  await clearPairingIdentity(pending.account.server, approval.pairingId);
  await clearRegistration(pending);
}
