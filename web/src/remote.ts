import { DirectEngine } from "./transport/direct-engine";
import type { RouteHint } from "./transport/direct-routing";
import { watchSecurityInstall } from "./device-security-store";
import { peerFrameBody } from "./transport/peer-frame";
import { RESULT_RETENTION_MS } from "./result-retention.gen";
import { abortable, ownedRound, PathChanged } from "./transport/recovery";
import { cleanup } from "./cleanup";
import { Link as DeviceLink } from "./transport/link";
import { RelayPath } from "./transport/relay-path";
import type { FrameStream } from "./transport/path";
import { machineNotificationKinds } from "./machine-notifications";
import * as l10n from "./i18n";
import { rejectRetiredHistory, UnsupportedHistoryError } from "./crypto";
import { frequency, ScheduleSeen, taskVisible, taskGroup, type ScheduleTask, type ScheduleSpec, type ScheduleDetailVisit, type TaskEntry } from "./schedules";
import { ComposerDraftStore, DraftTexts } from "./composer-drafts";
import { readSecurityOperation } from "./security-operation";
import { AuthorityClient, PairingDeliveryError, handoffCandidates as verifiedHandoffCandidates, type HandoffOffer } from "./authority-client";
import type { AuthorityPairing } from "./authority-pairing";
import { clearLoginToken, loadLoginToken, saveLoginToken } from "./device-security-store";
import { machineReplacementOrigins } from "./machine-replacement";
import { getCore } from "./core/runtime";
import { getCoreHost, type CoreExecutor, type HostBridge, type StreamLegCallbacks } from "./core/host";
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
  EventSchema as CoreEventSchema,
  EventsAppendedFrameSchema,
  EventsPageFetchedEventSchema,
  ListsFetchedEventSchema,
  OutcomeSource,
  PlainAccountPrefsSchema,
  PlainDeviceSchema,
  PlainEventSchema,
  PlainMachineSchema,
  PlainSessionSchema,
  SecuritySnapshotEventSchema,
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
  type PlainAccountPrefs,
  type PlainDevice,
  type PlainEvent,
  type PlainMachine,
  type PlainSession,
  type RpcEffect,
  type SecuritySnapshotEvent,
  type TokenResultEvent,
} from "./gen/remoteagent/core/v1/app_pb";
import { ConnState as CoreConnState } from "./gen/remoteagent/core/v1/home_view_pb";
import type { SessionDataView, SyncSessionView, SyncStateView } from "./gen/remoteagent/core/v1/sync_view_pb";
import { ProjectionStateSchema, type ProjectionState } from "./gen/remoteagent/core/v1/projection_pb";
import { PlanProposedSchema } from "./gen/remoteagent/v1/event_pb";
import {
  LiveSnapshotSchema,
  type LiveSnapshot,
} from "./gen/remoteagent/v1/remote_pb";
import type { SecurityRecord } from "./device-security-store";
import { agentOf, deriveSource, encodeEventBatch, fromProjectionState, machineName, type Derived } from "./model";
import { DEFAULT_AGENT } from "./agents";
import { isSkeleton } from "./skeleton";
import { SharedFileManager, type SharedFileChunkResult } from "./shared-files";
import { sharedFileStore } from "./shared-file-store";
import {
  DeviceSecurityClient,
  RecoveryHistoryUnavailable,
  validateSecurityBody,
  type SecurityStorage,
  type HistoryResult,
} from "./device-security-client";
import {
  MAX_CLOCK_SKEW,
  requestIdentity,
  equal,
  parseHeader,
  Purpose,
  SecurityError,
  type Member,
  type SecurityHeader,
} from "./device-security";
import {
  AuthService,
  ClientService,
  PairingService,
  DeviceKind,
  DeviceRegistrationSchema,
  PutDeviceMetaRequestSchema,
  ListMachinesResponseSchema,
  ListRemoteSessionsResponseSchema,
  NotifyKind,
  PeerFrameSchema,
  ServerFrameSchema,
  TermHelloSchema,
  PairApprovalSchema,
  RegisterDeviceRequestSchema,
  AccountPrefsObjectSchema,
  type AccountPrefsObject,
  type Device,
  type Machine as RemoteMachine,
  type RemoteSession,
} from "./gen/remoteagent/v1/relay_pb";
import {
  create,
  equals,
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
  expect,
} from "./crypto";
import {
  EnvelopeSchema,
  HeaderSchema,
  type Envelope,
  PayloadKind,
} from "./gen/remoteagent/v1/envelope_pb";
import {
  Agent,
  EventSchema,
  ImageFormat,
  SendMode,
  SessionStatus,
  type Event,
} from "./gen/remoteagent/v1/event_pb";
import { matchesFormat, mimeOf } from "./imageview";
import {
  ImageUploadState,
  DaemonSettingsSchema,
  ListModelsResponseSchema,
  SessionSchema,
  type ImageStorageUsage,
  type DaemonSettings,
  type ListModelsResponse,
  type MachineReplacementProof,
  type Session,
} from "./gen/remoteagent/v1/local_pb";
import {
  CommandResultSchema,
  CommandSchema,
  DeviceMetaSchema,
  ImagePayloadSchema,
  MachineMetaSchema,
  ProjectPrefsSchema,
  TerminalFrameSchema,
  type CommandResult,
  type DeviceMeta,
  type MachineMeta,
  type ProjectPrefs,
  type TerminalFrame,
} from "./gen/remoteagent/v1/remote_pb";
import { describeBrowser, type DeviceInfo } from "./devices";
import { fileUploadUpdateRequired, type Prepare, type Tray } from "./attach";
import { modelLabel, type Outgoing } from "./model";
import { CACHE_PAGE_SIZE, eventCache, type CachedEvent, type EventCache } from "./eventcache";
import { PROTOCOL_VERSION } from "./protocol";
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
export interface History { backfilling?: boolean; unavailable?: number; loaded: boolean; ready: boolean; error: string; broken: boolean; }
export class ScheduleRevisionError extends Error {}

// --- Transitional Step 3/4 command helper (remove when Session & Schedules migrate to Core) ---

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

export interface AnswerState { index: number; ticked: number[]; last?: { key: string; slot: RequestSlot }; busy: string; error: string; unknown: boolean; }
const noAnswer: AnswerState = { index: 0, ticked: [], busy: "", error: "", unknown: false };

export function apiBase(server: string): string {
  const origin = checkServer(server);
  return import.meta.env.VITE_SAME_ORIGIN_API ? location.origin : origin;
}

export interface TermConn { send(frame: MessageInitShape<typeof TerminalFrameSchema>): void; close(): void; }
export type ConnState = "connecting" | "online" | "offline";
export type Link = "online" | "waiting" | "no-network" | "no-server";

export function readOnlyCommand(cmd: CommandKind): boolean {
  return !!cmd && (
    ["getSettings", "listModels", "listDirectories", "getLiveSnapshot", "getMachineStatus", "getImageUploadStatus", "getSharedFileChunk"].includes(cmd.case ?? "") ||
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
  const request = toBinary(RegisterDeviceRequestSchema, create(RegisterDeviceRequestSchema, { protocolVersion: PROTOCOL_VERSION, registration: reg, approval: enrollment.approval, approvalSignature: enrollment.approvalSignature }));
  await saveRegistration({ account: stored, request, security, replace });
  await resumeRegistration();
  return stored;
}

export function livePlanText(live: LiveSnapshot | undefined, events: readonly Event[], readable: (seq: bigint) => boolean): string {
  if (!live?.session?.planOpen || live.planEventSeq === 0n) return live?.planScreen ?? "";
  const seq = live.planEventSeq;
  let e = seq > 0n && seq <= BigInt(events.length) ? events[Number(seq) - 1] : undefined;
  if (e?.seq !== seq) for (let i = events.length - 1; i >= 0 && e?.seq !== seq; i--) e = events[i];
  if (e?.seq === seq && e.kind.case === "planProposed" && readable(seq) && e.kind.value.text.trim() && !isSkeleton(e.kind.value)) return e.kind.value.text;
  return live.planScreen;
}

type CacheRefItem = { kind: "machine"; raw: RemoteMachine } | { kind: "session"; raw: RemoteSession } | { kind: "events"; sessionId: string; raw: CachedEvent[] } | { kind: "event"; sessionId: string; raw: CachedEvent } | { kind: "prefs"; raw: AccountPrefsObject };
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
  private watchedSession?: { sessionId: string; view: SessionDataView };
  private readonly liveSnapshots = new Map<string, LiveSnapshot>();
  private readonly liveErrors = new Map<string, string>();
  private readonly liveJobs = new Map<string, Promise<void>>();
  private currentDerivations = new WeakMap<
    Derived,
    { sid: string; live?: LiveSnapshot; checkpoint?: LiveSnapshot; agent: Agent; events?: Event[]; value: Derived }
  >();
  private projectionDerivations = new WeakMap<ProjectionState, Derived>();
  private emptyDerivations = new Map<Agent, Derived>();

  // Cache references assigned while unpacking server/cache payloads; unconsumed refs are released after Core dispatch.
  private cacheRefSeq = 0;
  private cacheRefs = new Map<string, CacheRefItem>();
  private eventCacheRefs = new WeakMap<CoreEvent, string[]>();
  private persistedMachines = new Map<string, RemoteMachine>();
  private persistedSessions = new Map<string, RemoteSession>();
  private persistedOpen = new Set<string>();
  private sessionReplacements = new Map<string, MachineReplacementProof[]>();

  private trustedMembers = new Map<string, Member>();
  private archiveMachines = new Set<string>();
  private sessionSeeds = new Map<string, Uint8Array<ArrayBuffer>>();
  private recoveryObjectsPending = false;
  private descriptionPending = false;
  private needsDescribeSelf = false;
  private securityPushSeq = 1n;
  private devicesPushSeq = 1n;
  private reconcilingSecurity = 0;
  private coreSyncDepth = 0;
  private serverAhead = false;
  authorityDeviceId = "";
  clockWarning = "";
  authorityError = "";

  readonly security: DeviceSecurityClient;
  readonly authority: AuthorityClient;
  readonly direct: DirectEngine;
  readonly deviceLink: DeviceLink;
  readonly startPairing: (web: string, replacesDeviceId?: string) => Promise<AuthorityPairing>;

  protected api: Client<typeof ClientService>;
  private unwatchDirectInstall?: () => void;
  private unwatchSecurityPush?: () => void;
  private securityTimer?: ReturnType<typeof setInterval>;
  private foreground = true;
  private lastSecuritySyncAt = 0;
  private directTransfers = new Map<string, "image" | "file">();
  private directReadyCache = new Map<string, boolean>();
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
  private readonly openingSessionMap = new Map<string, SessionView>();

  // Legacy views/features kept on Remote until Steps 3-6
  private sharedFilesState?: SharedFileManager;
  private scheduleSeenState?: ScheduleSeen;
  private scheduleRequests = new Map<string, string>();
  private scheduleLoading = new Map<string, Promise<void>>();
  private scheduleAgain = new Set<string>();
  private scheduleLive = new Set<string>();
  readonly schedules = new Map<string, { tasks: ScheduleTask[]; revision: bigint; loading: boolean; error: string }>();
  readonly scheduleAttempts = new Map<
    string,
    Attempt<{ operation: string; taskId: string; revision: number; spec?: ScheduleSpec; requestId?: string; sessionId?: string }>
  >();
  readonly scheduleFocus = new Map<string, { seq: bigint; runId: string }>();

  network = typeof navigator === "undefined" || navigator.onLine !== false;
  removalVerified = false;
  devicesStatus: "loading" | "ready" | "error" = "loading";
  focusProject = "";
  readonly created = new Set<string>();
  readonly missing = new Set<string>();
  readonly sessionErrors = new Set<string>();
  readonly draftMachines = new Map<string, string>();
  readonly termFirst = new Set<string>();
  readonly dockClosed = new Set<string>();
  readonly dockSeen = new Set<string>();
  readonly cancels = new Map<string, { state: "canceling" | "failed"; error: string }>();
  readonly resumes = new Map<string, { state: "sending" | "failed"; error: string }>();
  readonly scroll = new Map<string, { top: number; stick: boolean }>();
  readonly replaced = new Map<string, string>();
  drafts = new DraftTexts((key) => this.persistDraft(key));
  readonly approvalReasons = new Map<string, string>();
  readonly trays = new Map<string, Tray>();
  readonly outboxes = new Map<string, Outgoing[]>();
  private readonly forwardingOutbox = new Set<string>();
  private localDrafts?: ComposerDraftStore;
  private readonly forgottenSessions = new Set<string>();
  private readonly draftLoaded = new Set<string>();
  private readonly draftLoads = new Map<string, Promise<void>>();
  private readonly draftRevisions = new Map<string, number>();
  private readonly outboxLoads = new Map<string, Promise<void>>();
  private readonly outboxSaves = new Map<string, Promise<void>>();
  private readonly outboxWriteFailed = new Set<string>();
  private readonly localNotices = new Map<string, { message: string; kind: "draft" | "outbox_save" }>();
  private readonly answers = new Map<string, AnswerState>();
  readonly prompts = new Map<string, string>();
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
  imageStorage = new Map<string, ImageStorageUsage>();
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
    this.security = new DeviceSecurityClient(
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
    this.deviceLink = new DeviceLink(
      new RelayPath(
        (id) => {
          const m = this.machines.get(id);
          const direct = this.direct?.ready(id, { kind: "read" }) ?? false;
          return {
            relayRequired: !direct,
            unlinked: this.unlinked,
            reachable: direct || !!m?.online,
            ready: direct || this.conn === "online",
            authorized: !this.removed && !(this.archiveMachines.has(id) && !this.trustedMembers.has(id)),
            protocolVersion: direct ? PROTOCOL_VERSION : m?.meta?.protocolVersion,
            capabilities: this.direct?.capabilities(id) ?? m?.meta?.capabilities,
          };
        },
        () => this.relayEpoch(),
        (request, options) => this.api.forward(request, options),
        (signal) => this.login(signal),
      ),
    );
    this.direct = new DirectEngine(
      account,
      async () => {
        await this.checkNetworkSecurity();
        if (Date.now() - this.lastSecuritySyncAt >= 10_000) {
          await this.security.sync(undefined, 5000);
        }
        await this.security.verifiedRecord();
      },
      () => {
        if (!this.closed) {
          this.syncDirectStatuses();
          this.changed();
        }
      },
    );
    this.security.onSync = (record, started) => {
      this.lastSecuritySyncAt = started;
      void this.direct.synced(record, started);
      this.applySecurityRecordSets(record);
      if (this.coreSyncDepth === 0 && !this.closed) {
        const pushSeq = this.securityPushSeq++;
        const snap = this.snapshotFromRecord(0n, BigInt(this.networkGeneration), record, pushSeq);
        this.hostBridge?.dispatchEvent(
          create(CoreEventSchema, { event: { case: "securitySnapshot", value: snap } }),
        );
      }
    };
    this.security.onSyncFailed = () => this.direct.invalidate();
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
    this.authority = new AuthorityClient(
      account,
      this.security,
      this.api,
      pairing,
      (publicKey, check, persist, request) => this.publishHandoffOffer(publicKey, check, persist, request),
      undefined,
      () => void this.watchPairingDelivery(),
    );
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
      () => this.sharedFilesState?.onConnectivityChange(),
      ...[...this.listeners],
    );
  }

  private pairingTimer?: ReturnType<typeof setInterval>;
  private pairingBusy = false;
  private started = false;

  start() {
    this.started = true;
    this.closed = false;
    this.unwatchDirectInstall?.();
    this.unwatchDirectInstall = watchSecurityInstall((record) => this.direct.install(record));
    this.unwatchSecurityPush?.();
    this.unwatchSecurityPush = watchSecurityPush(() => {
      if (!this.closed) void this.security.sync(undefined, 5000).catch(() => undefined);
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
    this.direct.close();
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
    this.direct.setForeground(active);
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
    this.sessionErrors.delete(id);
    this.ensureHost().retrySession(id);
  }

  retryEvents(id: string) {
    this.ensureHost().retryEvents(id);
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

  // --- Transitional SyncStateView / SessionDataView projections ---

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

    const nextSessions = new Map<string, SessionView>();
    for (const sv of view.sessions) {
      this.openingSessionMap.delete(sv.sessionId);
      if (sv.info?.machineReplacements.length) this.sessionReplacements.set(sv.sessionId, [...sv.info.machineReplacements]);
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
    if (this.hostBridge) this.syncSchedules();
    this.changed();
    if (!wasOnline && (this.conn as ConnState) === "online" && !this.closed) void restorePushPrefs(this);
  }

  applySessionDataView(sid: string, view: SessionDataView) {
    this.watchedSession = { sessionId: sid, view };
    if (view.liveSnapshot) { this.liveSnapshots.set(sid, view.liveSnapshot); this.liveErrors.delete(sid); }
    this.reconcileOutbox(sid, view.events);
    this.changed();
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
    if (v.removed || v.connection === CoreConnState.OFFLINE || v.unlinked) return "no-server";
    return v.connection === CoreConnState.CONNECTED ? "online" : "waiting";
  }
  get showWaiting(): boolean { return this.conn !== "online" && this.link === "waiting"; }
  get awaiting(): boolean { return (this.syncStateView?.connection ?? CoreConnState.CONNECTING) === CoreConnState.CONNECTING; }
  get unlinked(): boolean { return this.link === "no-network" || this.link === "no-server" || (this.link === "waiting" && !this.listed); }
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
  protected trackOpeningSession(machineId: string, info?: Session) {
    if (!info?.id || this.cachedSessionMap.has(info.id)) return;
    this.openingSessionMap.set(info.id, { id: info.id, machineId, info, metaRev: 0n, lastSeq: 0n, updatedMs: BigInt(Date.now()), endedMs: 0n });
  }
  get openingSessions(): ReadonlyMap<string, SessionView> { return this.openingSessionMap; }
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
  events(sid: string): Event[] { return this.watchedSession?.sessionId === sid ? this.watchedSession.view.events : []; }
  loaded(sid: string): boolean { return this.watchedSession?.sessionId === sid && this.watchedSession.view.loaded; }
  history(sid: string): History {
    const sd = this.watchedSession?.sessionId === sid ? this.watchedSession.view : undefined;
    if (!sd) return { loaded: false, ready: false, error: "", broken: false };
    return { backfilling: sd.backfilling, unavailable: sd.unavailableCount, loaded: sd.loaded, ready: !sd.awaitingCache && (sd.loaded || sd.tailRequested || sd.events.length > 0), error: renderText(sd.error) || renderText(sd.fetchError), broken: sd.retired || sd.retryingCorruptCache };
  }

  private lastProjectionCache?: { state: ProjectionState; derived: Derived };
  private memoProjection(state: ProjectionState): Derived {
    let cached = this.projectionDerivations.get(state);
    if (!cached) {
      if (this.lastProjectionCache && equals(ProjectionStateSchema, this.lastProjectionCache.state, state)) cached = this.lastProjectionCache.derived;
      else { cached = fromProjectionState(state); this.lastProjectionCache = { state, derived: cached }; }
      this.projectionDerivations.set(state, cached);
    }
    return cached;
  }

  historicalDerived(sid: string): Derived {
    const sd = this.watchedSession?.sessionId === sid ? this.watchedSession.view : undefined;
    if (sd?.projection) return this.memoProjection(sd.projection);
    const agent = agentOf(this.sessions.get(sid));
    let cached = this.emptyDerivations.get(agent);
    if (!cached) {
      const empty = getCore().newProjection(agent);
      try { cached = fromProjectionState(fromBinary(ProjectionStateSchema, empty.snapshot())); } finally { empty.free(); }
      this.emptyDerivations.set(agent, cached);
    }
    return cached;
  }

  currentDerived(sid: string, d: Derived): Derived {
    const sd = this.watchedSession?.sessionId === sid ? this.watchedSession.view : undefined;
    if (sd?.projection && (!this.liveSnapshots.has(sid) || this.liveSnapshots.get(sid) === sd.liveSnapshot)) return this.memoProjection(sd.projection);
    const checkpoint = this.sessions.get(sid)?.checkpoint;
    const live = this.liveSnapshots.get(sid) ?? sd?.liveSnapshot ?? checkpoint;
    const source = deriveSource(d);
    const events = source?.events ?? sd?.events;
    const agent = source?.agent ?? agentOf(this.sessions.get(sid));
    const cached = this.currentDerivations.get(d);
    if (cached && cached.sid === sid && cached.live === live && cached.checkpoint === checkpoint && cached.agent === agent && cached.events === events) return cached.value;
    const temp = getCore().newProjection(agent);
    let out: Derived;
    try {
      if (events?.length) temp.append(encodeEventBatch(events));
      out = fromProjectionState(fromBinary(ProjectionStateSchema, temp.currentActions(live ? toBinary(LiveSnapshotSchema, live) : undefined)));
    } finally { temp.free(); }
    if (live?.planEventSeq && live.planScreenHash.length === 32 && live.session?.planOpen && events) {
      const resolved = livePlanText(live, events, () => true);
      if (resolved !== live.planScreen || !out.plan?.text) out.plan = create(PlanProposedSchema, { text: resolved });
    }
    this.currentDerivations.set(d, { sid, live, checkpoint, agent, events, value: out });
    return out;
  }

  liveSnapshotError(sid: string): string | undefined {
    const s = this.sessions.get(sid);
    return this.liveErrors.get(sid) ?? (s?.checkpointStatus === "waiting" ? l10n.t("errors.waiting_for_the_owning_computer_to_update_the_current") : undefined);
  }

  async refreshLiveSnapshot(sid: string): Promise<void> {
    if (this.liveJobs.has(sid)) return this.liveJobs.get(sid);
    const job = (async () => {
      try {
        const s = this.sessions.get(sid);
        if (!s) throw new Error(l10n.t("session.android_unknown"));
        const reply = await this.forward(s.machineId, sid, { case: "getLiveSnapshot", value: { sessionId: sid } });
        if (reply.kind.case !== "liveSnapshot" || reply.kind.value.session?.id !== sid) throw new Error(l10n.t("errors.invalid_current_request_snapshot"));
        this.liveSnapshots.set(sid, reply.kind.value);
        this.liveErrors.delete(sid);
      } catch {
        this.liveSnapshots.delete(sid);
        this.liveErrors.set(sid, l10n.t("errors.pending_requests_on_the_computer_could_not_be_verified"));
      } finally {
        this.liveJobs.delete(sid);
        this.changed();
      }
    })();
    this.liveJobs.set(sid, job);
    return job;
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
      this.direct.detach(ws);
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
        this.direct.attach(ws);
        callbacks.onReady(legId, frame.kind.value.serverTimeMs);
        return;
      }
      if (frame.kind.case === "peerFrameDelivery" || frame.kind.case === "peerFrameResult") {
        const raw = peerFrameBody(new Uint8Array(data), frame.kind.case === "peerFrameDelivery" ? 11 : 12);
        if (raw) { if (frame.kind.case === "peerFrameDelivery") this.direct.receive(raw, ws); else this.direct.result(raw, ws); }
        return;
      }
      queuedBytes += data.byteLength;
      queuedFrames++;
      if (queuedBytes > 8 * 1024 * 1024 || queuedFrames > 1024) { drop(l10n.t("errors.push_backlog_reconnecting")); return; }
      processing = processing.then(async () => {
        if (state.stopped || this.legs.get(legId) !== state) return;
        const refs: string[] = [];
        let dispatched = false;
        try {
          const mapped = await this.unpackStreamFrame(frame.kind, refs);
          if (!state.stopped && this.legs.get(legId) === state && mapped) {
            dispatched = true;
            callbacks.onFrame(legId, mapped, () => this.releaseCacheObjects(refs));
          }
        } finally { if (!dispatched) this.releaseCacheObjects(refs); }
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
    this.direct.detach(leg.ws);
    leg.ws.close();
  }

  private async unpackStreamFrame(kind: ReturnType<typeof fromBinary<typeof ServerFrameSchema>>["kind"], refs: string[]) {
    switch (kind.case) {
      case "heartbeat": return { case: "heartbeat" as const, value: true };
      case "machineUpdated": return kind.value.machine ? { case: "machineUpdated" as const, value: await this.unpackMachine(kind.value.machine, refs) } : undefined;
      case "machineRemoved": this.forgetSchedules(kind.value.machineId); return { case: "machineRemovedId" as const, value: kind.value.machineId };
      case "sessionUpdated": return kind.value.session ? { case: "sessionUpdated" as const, value: await this.unpackSession(kind.value.session, true, refs) } : undefined;
      case "eventsAppended": return { case: "eventsAppended" as const, value: create(EventsAppendedFrameSchema, { sessionId: kind.value.sessionId, events: await this.unpackEvents(kind.value.sessionId, kind.value.events, false, refs) }) };
      case "accountPrefsUpdated": return { case: "accountPrefsUpdated" as const, value: await this.unpackAccountPrefs(kind.value.object, refs) };
      case "deviceUpdated": return kind.value.device ? { case: "deviceUpdated" as const, value: await this.unpackDevice(kind.value.device) } : undefined;
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

  private applySecurityRecordSets(record: SecurityRecord) {
    const head = record.states[record.states.length - 1];
    this.authorityDeviceId = head.core.authorityID;
    this.trustedMembers = new Map(head.core.members.map((m) => [m.id, m]));
    this.archiveMachines = new Set(record.states.flatMap((s) => s.core.members.filter((m) => m.kind === 1).map((m) => m.id)));
    for (const id of this.archiveMachines) if (!this.trustedMembers.has(id)) this.forgetSchedules(id);
  }

  private snapshotFromRecord(opId: bigint, networkGeneration: bigint, record: SecurityRecord, pushSeq = 0n): SecuritySnapshotEvent {
    const head = record.states[record.states.length - 1];
    if (record.member === false) {
      return create(SecuritySnapshotEventSchema, { opId, pushSeq, isMember: false, selfDeviceId: this.account.deviceId, authorityDeviceId: this.authorityDeviceId, clockSkewed: this.serverAhead, networkGeneration });
    }
    return create(SecuritySnapshotEventSchema, {
      opId, pushSeq, isMember: true, selfDeviceId: this.account.deviceId, authorityDeviceId: this.authorityDeviceId,
      trustedMachineIds: head.core.members.filter((m) => m.kind === 1).map((m) => m.id),
      trustedClientIds: head.core.members.filter((m) => m.kind === 2).map((m) => m.id),
      archiveMachineIds: [...this.archiveMachines].filter((id) => this.trustedMembers.get(id)?.kind !== 1),
      headEpoch: head.core.epoch, leaseEndMs: record.lease?.notAfter ?? 0n, clockSkewed: this.serverAhead,
      recoveryPending: this.security.recoveryPending, recoveryReadyMs: this.security.recoveryReadyMs, recoveryObjectsPending: this.recoveryObjectsPending, networkGeneration,
    });
  }

  async syncSecurity(opId: bigint, networkGeneration: bigint, touch: boolean): Promise<SecuritySnapshotEvent> {
    const pushSeq = opId === 0n ? this.securityPushSeq++ : 0n;
    const gen = Number(networkGeneration);
    if (gen > this.networkGeneration) { this.networkGeneration = gen; this.retireNetworkIO(); }
    if (touch) this.touchAfterWake();
    this.coreSyncDepth++;
    try {
      const record = await this.security.sync(undefined, 5000);
      this.verifiedNetworkGeneration = this.networkGeneration;
      this.wakeRequired = false;
      this.applySecurityRecordSets(record);
      this.maintainAuthority();
      if (this.recoveryObjectsPending) {
        try { await this.republishRecoveryObjects(record, this.api); this.recoveryObjectsPending = false; }
        catch { /* Retry on subsequent security sync. */ }
      }
      if (this.needsDescribeSelf) await this.describeSelf();
      if (record.member === false) { this.removalVerified = true; this.discardToken(); await this.tokenWrites; }
      return this.snapshotFromRecord(opId, networkGeneration, record, pushSeq);
    } catch (e) {
      return create(SecuritySnapshotEventSchema, {
        opId, pushSeq, selfDeviceId: this.account.deviceId,
        clockSkewed: this.serverAhead || (e instanceof SecurityError && e.code === "clock"),
        recoveryPending: this.security.recoveryPending, recoveryReadyMs: this.security.recoveryReadyMs,
        recoveryObjectsPending: this.recoveryObjectsPending, networkGeneration, error: errorText(e),
      });
    } finally { this.coreSyncDepth--; }
  }

  private stashCacheObject(item: CacheRefItem, refs?: string[]): string {
    if (!this.cache) return "";
    const id = `r${++this.cacheRefSeq}`;
    this.cacheRefs.set(id, item);
    refs?.push(id);
    return id;
  }

  private releaseCacheObjects(refs: string[]) { for (const ref of refs) this.cacheRefs.delete(ref); }

  private trackEventRefs<T extends CoreEvent>(ev: T, refs: string[]): T {
    if (refs.length > 0) this.eventCacheRefs.set(ev, refs);
    return ev;
  }

  releaseEventRefs(ev: CoreEvent) {
    const refs = this.eventCacheRefs.get(ev);
    if (refs) { this.eventCacheRefs.delete(ev); this.releaseCacheObjects(refs); }
  }

  async readBootstrapCache(opId: bigint): Promise<CacheBootstrapLoadedEvent> {
    await this.warmInfoOnce();
    const homeScopeToken = readStoredScopeToken();
    let scopeHmacKey = new Uint8Array(32);
    const machines: PlainMachine[] = [], sessions: PlainSession[] = [];
    let prefs: PlainAccountPrefs | undefined;
    if (!this.cache) { crypto.getRandomValues(scopeHmacKey); return create(CacheBootstrapLoadedEventSchema, { opId, homeScopeToken, scopeHmacKey }); }
    try {
      const existingKey = await this.cache.get(SCOPE_HMAC_KEY_VALUE);
      if (existingKey instanceof Uint8Array && existingKey.length === 32) scopeHmacKey = new Uint8Array(existingKey);
      else { crypto.getRandomValues(scopeHmacKey); await this.cache.set(SCOPE_HMAC_KEY_VALUE, scopeHmacKey); }
      const identity = await this.security.localIdentity();
      const rec = await this.security.verifiedRecord();
      this.trustedMembers = new Map(identity.state.core.members.map((m) => [m.id, m]));
      this.authorityDeviceId = identity.state.core.authorityID;
      this.archiveMachines = new Set(rec.states.flatMap((s) => s.core.members.filter((m) => m.kind === 1).map((m) => m.id)));
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
          const unpacked = await this.unpackMachine(m, undefined, true);
          machines.push(unpacked);
          if (unpacked.meta?.capabilities?.scheduledTasks === true && !(this.archiveMachines.has(m.id) && !this.trustedMembers.has(m.id))) {
            const keptSched = await this.cache.get("schedules:" + m.id);
            if (valid(keptSched) && keptSched.bytes instanceof Uint8Array) void this.restoreScheduleSnapshot(m.id, keptSched, true).catch(() => undefined);
          }
        }
      }
      if (valid(keptSessions) && keptSessions.bytes instanceof Uint8Array) {
        for (const rs of fromBinary(ListRemoteSessionsResponseSchema, keptSessions.bytes).sessions) {
          this.persistedSessions.set(rs.sessionId, rs);
          sessions.push(await this.unpackSession(rs, true, undefined, true));
        }
      }
      if (valid(keptPrefs) && keptPrefs.bytes instanceof Uint8Array) {
        prefs = await this.unpackAccountPrefs(fromBinary(AccountPrefsObjectSchema, keptPrefs.bytes), undefined, true);
      }
      return create(CacheBootstrapLoadedEventSchema, { opId, machines, sessions, prefs, homeScopeToken, scopeHmacKey });
    } catch (e) {
      return create(CacheBootstrapLoadedEventSchema, { opId, machines, sessions, prefs, homeScopeToken, scopeHmacKey, error: errorText(e) });
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
      const events = await this.unpackEvents(sessionId, rows, true);
      return create(CacheEventsLoadedEventSchema, { opId, sessionId, events, broken: events.some((e) => e.broken), retired: events.some((e) => e.retired) });
    } catch (e) {
      return create(CacheEventsLoadedEventSchema, { opId, sessionId, error: errorText(e) });
    }
  }

  private extractCacheWrite(eff: CacheWriteEffect): CacheWritePayload | undefined {
    const take = (ref: string) => { const item = this.cacheRefs.get(ref); this.cacheRefs.delete(ref); return item; };
    switch (eff.kind.case) {
      case "writeMachineRef": { const it = take(eff.kind.value); return it?.kind === "machine" ? { kind: "machine", raw: it.raw } : undefined; }
      case "writeSessionRef":
      case "keepSessionOwnerRef": { const it = take(eff.kind.value); return it?.kind === "session" ? { kind: "session", raw: it.raw } : undefined; }
      case "writeEvents": {
        if (!this.cache) return undefined;
        const rows: CachedEvent[] = [];
        for (const ref of eff.kind.value.cacheRefs) {
          const it = take(ref);
          if (it?.kind === "event") rows.push(it.raw);
          else if (it?.kind === "events") rows.push(...it.raw);
        }
        return rows.length > 0 ? { kind: "events", sessionId: eff.kind.value.sessionId, raw: rows } : undefined;
      }
      case "writePrefsRef": { const it = take(eff.kind.value); return it?.kind === "prefs" ? { kind: "prefs", raw: it.raw } : undefined; }
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
        this.sessionSeeds.delete(eff.kind.value);
        this.forgetLocalSession(eff.kind.value);
        await this.persistSessionsList();
        await this.keepValue("open-sessions", { ids: [...this.persistedOpen] });
        break;
      case "unlistSessionId": this.persistedSessions.delete(eff.kind.value); await this.persistSessionsList(); break;
      case "removeMachineId": this.persistedMachines.delete(eff.kind.value); await this.persistMachinesList(); this.forgetSchedules(eff.kind.value); break;
    }
  }

  private persistMachinesList = () => this.keepValue("machines", { bytes: toBinary(ListMachinesResponseSchema, create(ListMachinesResponseSchema, { machines: [...this.persistedMachines.values()] })) });
  private persistSessionsList = () => this.keepValue("sessions", { bytes: toBinary(ListRemoteSessionsResponseSchema, create(ListRemoteSessionsResponseSchema, { sessions: [...this.persistedSessions.values()] })) });

  private rpcEv(event: CoreEvent["event"], refs: string[] = []): CoreEvent {
    return this.trackEventRefs(create(CoreEventSchema, { event }), refs);
  }

  async executeRpc(eff: RpcEffect): Promise<CoreEvent> {
    const opId = eff.opId;
    const refs: string[] = [];
    const fail = (e: unknown, fallback: CoreEvent["event"]) => {
      this.releaseCacheObjects(refs);
      if (fallback.case === "eventsPageFetched") fallback.value.error = errorText(e);
      return create(CoreEventSchema, { event: fallback });
    };
    switch (eff.kind.case) {
      case "fetchLists":
        try {
          const [mRes, sRes, dRes] = await Promise.all([
            this.api.listMachines({}),
            this.api.listRemoteSessions({ limit: eff.kind.value.limit || this.sessionPageSize }),
            this.api.listDevices({}),
          ]);
          await this.maybeRepublishAndDescribe(dRes.devices);
          const machines = await Promise.all(mRes.machines.map((m) => this.unpackMachine(m, refs)));
          const sessions = await Promise.all(sRes.sessions.map((s) => this.unpackSession(s, true, refs)));
          const devices = await this.unpackMemberDevices(dRes.devices);
          return this.rpcEv({ case: "listsFetched", value: create(ListsFetchedEventSchema, { opId, machines, sessions, next: sRes.next, devices }) }, refs);
        } catch (e) {
          this.releaseCacheObjects(refs);
          return this.rpcEv({ case: "listsFetched", value: create(ListsFetchedEventSchema, { opId, error: errorText(e) }) });
        }
      case "fetchSessionsPage":
        try {
          const res = await this.api.listRemoteSessions({ after: eff.kind.value.after, limit: eff.kind.value.limit || this.sessionPageSize });
          const sessions = await Promise.all(res.sessions.map((s) => this.unpackSession(s, true, refs)));
          return this.rpcEv({ case: "sessionsPageFetched", value: create(SessionsPageFetchedEventSchema, { opId, sessions, next: res.next }) }, refs);
        } catch (e) {
          this.releaseCacheObjects(refs);
          return this.rpcEv({ case: "sessionsPageFetched", value: create(SessionsPageFetchedEventSchema, { opId, error: errorText(e) }) });
        }
      case "fetchSessionById": {
        const sid = eff.kind.value.sessionId;
        try {
          const res = await this.api.listRemoteSessions({ sessionIds: [sid], limit: 1 });
          const found = res.sessions.find((s) => s.sessionId === sid);
          const session = found ? await this.unpackSession(found, true, refs) : undefined;
          this.sessionErrors.delete(sid);
          return this.rpcEv({ case: "sessionByIdFetched", value: create(SessionByIdFetchedEventSchema, { opId, session }) }, refs);
        } catch (e) {
          this.releaseCacheObjects(refs);
          this.sessionErrors.add(sid);
          return this.rpcEv({ case: "sessionByIdFetched", value: create(SessionByIdFetchedEventSchema, { opId, error: errorText(e) }) });
        }
      }
      case "getEvents": {
        const { sessionId, afterSeq, limit } = eff.kind.value;
        try {
          const res = await this.api.getEvents({ sessionId, afterSeq, limit: limit || this.historyPageSize });
          const events = await this.unpackEvents(sessionId, res.events, true, refs);
          return this.rpcEv({ case: "eventsPageFetched", value: create(EventsPageFetchedEventSchema, { opId, sessionId, events, more: res.more }) }, refs);
        } catch (e) {
          return fail(e, { case: "eventsPageFetched", value: create(EventsPageFetchedEventSchema, { opId, sessionId }) });
        }
      }
      case "getAccountPrefs":
        try {
          const res = await this.api.getAccountPrefs({ objectId: eff.kind.value.objectId || PREFS_OBJECT });
          return this.rpcEv({ case: "accountPrefsFetched", value: create(AccountPrefsFetchedEventSchema, { opId, prefs: await this.unpackAccountPrefs(res.object, refs) }) }, refs);
        } catch (e) {
          this.releaseCacheObjects(refs);
          return this.rpcEv({ case: "accountPrefsFetched", value: create(AccountPrefsFetchedEventSchema, { opId, error: errorText(e) }) });
        }
      case "putAccountPrefs": {
        const { objectId, expectedRevision, nextPrefs } = eff.kind.value;
        try {
          const env = await this.sealSecureMessage({ kind: PayloadKind.ACCOUNT_PREFS, requestId: objectId || PREFS_OBJECT, seq: expectedRevision + 1n, sender: this.account.deviceId }, ProjectPrefsSchema, nextPrefs ?? emptyPrefs());
          const res = await this.api.putAccountPrefs({ objectId: objectId || PREFS_OBJECT, expectedRevision, envelope: env });
          return this.rpcEv({ case: "accountPrefsPutResult", value: create(AccountPrefsPutResultEventSchema, { opId, applied: res.applied, current: await this.unpackAccountPrefs(res.object, refs) }) }, refs);
        } catch (e) {
          this.releaseCacheObjects(refs);
          const sealConflict = e instanceof SecurityError && (e.code === "clock" || e.code === "lease" || e.code === "conflict");
          return this.rpcEv({ case: "accountPrefsPutResult", value: create(AccountPrefsPutResultEventSchema, { opId, sealConflict, error: errorText(e) }) });
        }
      }
      case "fetchDevices":
        try {
          const res = await this.api.listDevices({});
          await this.maybeRepublishAndDescribe(res.devices);
          return this.rpcEv({ case: "devicesFetched", value: create(DevicesFetchedEventSchema, { opId, devices: await this.unpackMemberDevices(res.devices) }) });
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

  async executeCommandAttempt(eff: DispatchCommandEffect, signal?: AbortSignal): Promise<CommandOutcomeEvent> {
    let hint: RouteHint | undefined;
    if (eff.routeHint === CommandRouteHint.READ) hint = { kind: "read" };
    else if (eff.routeHint === CommandRouteHint.BULK) hint = { kind: "bulk", file: eff.bulkFileKey, upload: eff.bulkUpload ? "file" : undefined, bytes: Number(eff.bulkBytes) };
    const { outcome, msg } = await this.executeSealedAttempt({
      requestId: eff.requestId, attemptId: eff.attemptId, machineId: eff.machineId, sessionId: eff.sessionId,
      deadlineMs: Number(eff.deadlineMs), commandBytes: new Uint8Array(eff.commandBytes),
      routeHint: hint, forceRelay: eff.forceRelay, priorUnknown: eff.retryOfUnknown, signal,
    });
    if ((msg?.kind.case === "createSession" || msg?.kind.case === "forkSession") && msg.kind.value.session) {
      this.trackOpeningSession(eff.machineId, msg.kind.value.session);
    }
    return outcome;
  }

  private async executeSealedAttempt(params: {
    requestId: string; attemptId: string; machineId: string; sessionId: string; deadlineMs: number;
    commandBytes: Uint8Array<ArrayBuffer>; routeHint?: RouteHint; forceRelay: boolean; priorUnknown: boolean;
    isScheduleList?: boolean; hasFiles?: boolean; signal?: AbortSignal; onHandoff?: () => () => void;
  }): Promise<{ outcome: CommandOutcomeEvent; msg?: CommandResult; rawEnvelope?: Envelope; directLegFailed?: boolean }> {
    const base = { requestId: params.requestId, attemptId: params.attemptId };
    const mk = (fields: Omit<Partial<CommandOutcomeEvent>, "$typeName" | "requestId" | "attemptId">) =>
      create(CommandOutcomeEventSchema, { ...base, ...fields });
    if (this.closed) return { outcome: mk({ source: OutcomeSource.LOCAL, notForwarded: true, transportCode: "canceled" }) };
    if (this.serverAhead) return { outcome: mk({ source: OutcomeSource.LOCAL, notForwarded: !params.priorUnknown, transportCode: params.priorUnknown ? "unknown" : "clock", transportDetail: l10n.t("errors.this_device_s_clock_is_behind_the_account_state_remote") }) };

    let parsedCmd: ReturnType<typeof fromBinary<typeof CommandSchema>> | undefined;
    try { parsedCmd = fromBinary(CommandSchema, params.commandBytes); } catch { parsedCmd = undefined; }

    let envelope: Envelope, binding: { key: Uint8Array; payload: Uint8Array };
    try {
      await this.checkNetworkSecurity();
      envelope = await this.sealCommandBytes(params.machineId, params.sessionId, params.requestId, BigInt(params.deadlineMs), params.commandBytes);
      binding = await requestIdentity(parseHeader(new Uint8Array(envelope.securityHeader)), params.commandBytes);
      if (params.hasFiles) {
        const issue = this.fileUploadIssue(params.machineId);
        if (issue) return { outcome: mk({ source: OutcomeSource.LOCAL, notForwarded: true, transportCode: params.priorUnknown ? "unknown" : "failed_precondition", transportDetail: issue }) };
      }
    } catch (e) {
      this.noteClock(e);
      if (params.priorUnknown) return { outcome: mk({ source: OutcomeSource.LOCAL, notForwarded: false, transportCode: "unknown", transportDetail: errorText(e) }) };
      const code = e instanceof SecurityError && e.code === "clock" ? "clock" : e instanceof SecurityError && e.code === "lease" ? "failed_precondition" : "security_unavailable";
      return { outcome: mk({ source: OutcomeSource.LOCAL, notForwarded: true, transportCode: code, transportDetail: errorText(e) }) };
    }

    type LegResult = { outcome: CommandOutcomeEvent; msg?: CommandResult; rawEnvelope?: Envelope; handedOff: boolean; legAttempt: { direct?: boolean } };
    const routeAttemptWith = (attemptBuilder: (handoff: () => void) => ReturnType<DeviceLink["router"]["path"]["attempt"]> | undefined, tracking?: ReturnType<DirectEngine["track"]>) => {
      let handedOff = false, undo: (() => void) | undefined;
      const attempt = attemptBuilder(() => { handedOff = true; undo = params.onHandoff?.(); });
      if (!attempt) return undefined;
      return {
        attempt,
        handedOff: () => handedOff,
        undo: () => { undo?.(); undo = undefined; },
        tracking,
      };
    };

    const executeLeg = async (
      leg: NonNullable<ReturnType<typeof routeAttemptWith>>,
      legSignal?: AbortSignal,
    ): Promise<LegResult> => {
      const a = leg.attempt;
      const isDirect = !!a.direct;
      const routeStarted = performance.now();
      try {
        legSignal?.throwIfAborted();
        await a.prepare();
        const res = await a.send(params.machineId, envelope, legSignal);
        let msg: CommandResult;
        try {
          msg = (await this.openSecureMessage(res.result, { kind: PayloadKind.COMMAND_RESULT, machineId: params.machineId, sessionId: params.sessionId, requestId: params.requestId }, CommandResultSchema)).msg;
        } catch {
          return { handedOff: leg.handedOff(), legAttempt: a, outcome: mk({ source: OutcomeSource.DAEMON, attemptBound: res.attemptId === a.attemptId, signed: false, hashesMatch: false, routeWasDirect: isDirect }) };
        }
        const hashesMatch = equal(msg.requestKeyHash, binding.key) && equal(msg.payloadHash, binding.payload) && (msg.outcome === "conflict" || msg.conflictKind === "");
        const attemptBound = res.attemptId === a.attemptId;
        const outerMatchesInner = isDirect || (res.outcome === msg.outcome && res.reason === msg.reason);
        if (leg.tracking && hashesMatch && attemptBound && outerMatchesInner && !msg.errorCode && msg.outcome === "executed") {
          const bytes = parsedCmd?.kind.case === "putImageChunk" ? parsedCmd.kind.value.data?.length ?? 0 : msg.kind.case === "getSharedFileChunk" ? msg.kind.value.data.length : 0;
          leg.tracking.observe(isDirect, bytes, performance.now() - routeStarted);
        }
        if (parsedCmd?.kind.case === "beginImageUpload") {
          if (this.directTransfers.size >= 128) this.directTransfers.delete(this.directTransfers.keys().next().value!);
          this.directTransfers.set(`${params.machineId}/${parsedCmd.kind.value.transferId}`, parsedCmd.kind.value.fileName ? "file" : "image");
        } else if (parsedCmd?.kind.case === "completeImageUpload" || parsedCmd?.kind.case === "abortImageUpload") {
          this.directTransfers.delete(`${params.machineId}/${parsedCmd.kind.value.transferId}`);
        }
        if (params.isScheduleList && res.result && this.machines.has(params.machineId) && this.supportsSchedules(params.machineId)) {
          await this.keepScheduleSnapshot(params.machineId, { bytes: toBinary(EnvelopeSchema, res.result), requestId: params.requestId });
        }
        return {
          handedOff: leg.handedOff(), legAttempt: a, msg, rawEnvelope: res.result,
          outcome: mk({ source: OutcomeSource.DAEMON, signed: true, hashesMatch, attemptBound, outerMatchesInner, outerOutcome: isDirect ? msg.outcome : res.outcome, outerReason: isDirect ? msg.reason : res.reason, result: msg, routeWasDirect: isDirect }),
        };
      } catch (e) {
        if (isDirect) leg.tracking?.failed();
        if (e instanceof PathChanged) return { handedOff: leg.handedOff(), legAttempt: a, outcome: mk({ source: OutcomeSource.TRANSPORT, notForwarded: !leg.handedOff(), transportCode: "path_changed", routeWasDirect: isDirect }) };
        if (e instanceof ConnectError) {
          const refusal = a.refusal(e);
          if (refusal !== undefined) {
            leg.undo();
            return { handedOff: false, legAttempt: a, outcome: mk({ source: OutcomeSource.SERVER, notForwarded: true, attemptBound: true, outerReason: refusal.reason ?? "", transportCode: connectErrorCode(e.code), transportDetail: refusal.reason === "machine_offline" ? "" : (e.rawMessage || e.message), routeWasDirect: false }) };
          }
          if (!leg.handedOff() && e.code === Code.Unauthenticated) {
            return { handedOff: false, legAttempt: a, outcome: mk({ source: OutcomeSource.SERVER, notForwarded: true, attemptBound: true, transportCode: "unauthenticated", transportDetail: e.rawMessage || e.message, routeWasDirect: false }) };
          }
          return { handedOff: leg.handedOff(), legAttempt: a, outcome: mk({ source: OutcomeSource.SERVER, notForwarded: !leg.handedOff(), attemptBound: false, transportCode: connectErrorCode(e.code), transportDetail: e.rawMessage || e.message, routeWasDirect: false }) };
        }
        return { handedOff: leg.handedOff(), legAttempt: a, outcome: mk({ source: OutcomeSource.TRANSPORT, notForwarded: !leg.handedOff(), transportCode: !this.network ? "network" : "unknown", transportDetail: errorText(e), routeWasDirect: isDirect }) };
      }
    };

    const tracking = params.routeHint ? this.direct.track(params.machineId, params.routeHint) : undefined;
    const interactive = isInteractiveCommand(parsedCmd?.kind);
    const dualDirect = !params.forceRelay && interactive && this.direct.ready(params.machineId, { kind: "read" })
      ? routeAttemptWith((h) => this.direct.attemptInteractive(params.machineId, h))
      : undefined;

    if (dualDirect?.attempt.direct) {
      let directLegFailed = false;
      const relayLeg = routeAttemptWith((h) => this.deviceLink.router.path.attempt(h))!;
      const isTrusted = (res: LegResult) => {
        const o = res.outcome;
        return o.source === OutcomeSource.DAEMON && o.signed && o.hashesMatch && o.attemptBound && o.outerMatchesInner;
      };
      try {
        const winner = await this.deviceLink.router.dual<LegResult>(
          async (legSignal) => {
            const res = await executeLeg(dualDirect, legSignal);
            if (isTrusted(res)) return { trusted: true, value: res };
            directLegFailed = true;
            return { trusted: false, error: res };
          },
          async (legSignal) => {
            const res = await executeLeg(relayLeg, legSignal);
            if (isTrusted(res)) return { trusted: true, value: res };
            return { trusted: false, error: res };
          },
          params.signal,
        );
        return { outcome: winner.outcome, msg: winner.msg, rawEnvelope: winner.rawEnvelope, directLegFailed };
      } catch (err) {
        const fallback = err as LegResult;
        const out = fallback.outcome;
        const serverNotForwarded = out.source === OutcomeSource.SERVER && out.notForwarded && out.attemptBound;
        const relayPossiblyForwarded = relayLeg.handedOff() && !serverNotForwarded;
        if (dualDirect.handedOff() || relayPossiblyForwarded) {
          out.notForwarded = false;
          if (out.source === OutcomeSource.LOCAL || out.source === OutcomeSource.SERVER) out.source = OutcomeSource.TRANSPORT;
        }
        return { outcome: out, msg: fallback.msg, rawEnvelope: fallback.rawEnvelope, directLegFailed };
      }
    }

    const primary = (params.routeHint && !params.forceRelay ? routeAttemptWith((h) => this.direct.attempt(params.machineId, params.routeHint!, h), tracking) : undefined)
      ?? routeAttemptWith((h) => this.deviceLink.router.path.attempt(h))!;
    const single = await executeLeg(primary, params.signal);
    return { outcome: single.outcome, msg: single.msg, rawEnvelope: single.rawEnvelope };
  }

  // --- Security Gate unpacking ---

  private async unpackMachine(m: RemoteMachine, refs?: string[], fromCache = false): Promise<PlainMachine> {
    const cacheRef = !fromCache ? this.stashCacheObject({ kind: "machine", raw: m }, refs) : "";
    let meta: MachineMeta | undefined, metaRev = 0n, metaBroken = false;
    if (m.meta) {
      try { ({ msg: meta, header: { seq: metaRev } } = await this.openSecureMessage(m.meta, { kind: PayloadKind.MACHINE_META, machineId: m.id }, MachineMetaSchema)); }
      catch (e) { metaBroken = true; this.noteClock(e); }
    }
    return create(PlainMachineSchema, { machineId: m.id, online: !fromCache && m.online, lastSeenMs: m.lastSeenMs, meta, metaRev, metaBroken, cacheRef });
  }

  private async unpackSession(rs: RemoteSession, _historyAllowed: boolean, refs?: string[], fromCache = false): Promise<PlainSession> {
    const cacheRef = !fromCache ? this.stashCacheObject({ kind: "session", raw: rs }, refs) : "";
    let meta: Session | undefined, metaRev = 0n, checkpoint: LiveSnapshot | undefined, checkpointInvalid = false, retiredHistory = false, openFailed = false;
    try {
      rejectRetiredHistory(rs.key);
      if (rs.key) {
        const opened = await this.security.openHistory(rs.key);
        const header = this.projectHeader(opened.header);
        if (opened.plaintext.length !== 32) throw new Error(l10n.t("errors.invalid_session_seed"));
        expect(header, { kind: PayloadKind.SESSION_KEY, machineId: rs.machineId, sessionId: rs.sessionId });
        this.sessionSeeds.set(rs.sessionId, new Uint8Array(opened.plaintext));
      }
      if (rs.meta) {
        ({ msg: meta, header: { seq: metaRev } } = await this.openSecureMessage(rs.meta, { kind: PayloadKind.SESSION_META, machineId: rs.machineId, sessionId: rs.sessionId }, SessionSchema));
        if (meta.machineReplacements.length) this.sessionReplacements.set(rs.sessionId, [...meta.machineReplacements]);
        if (meta.currentCheckpoint.length) {
          const cp = fromBinary(LiveSnapshotSchema, meta.currentCheckpoint);
          if (cp.session?.id !== rs.sessionId || cp.session.lastSeq > meta.lastSeq || cp.actions.some((e) => e.sessionId !== rs.sessionId || e.seq > meta!.lastSeq)) checkpointInvalid = true;
          else checkpoint = cp;
        }
      }
    } catch (e) {
      if (e instanceof UnsupportedHistoryError) retiredHistory = true;
      else { openFailed = true; this.noteClock(e); }
    }
    return create(PlainSessionSchema, { sessionId: rs.sessionId, machineId: rs.machineId, lastSeq: rs.lastSeq, updatedMs: rs.updatedMs, listedEndedMs: rs.endedMs, meta, metaRev, checkpoint, checkpointInvalid, retiredHistory, openFailed, cacheRef });
  }

  private async unpackMemberDevices(serverDevices: Device[]): Promise<PlainDevice[]> {
    try {
      const head = (await this.security.verifiedRecord()).states.at(-1);
      if (head) {
        return await Promise.all(head.core.members.map((m) => {
          const hint = serverDevices.find((d) => d.id === m.id);
          return hint ? this.unpackDevice(hint) : Promise.resolve(create(PlainDeviceSchema, { deviceId: m.id, kind: m.kind }));
        }));
      }
    } catch { /* Fall back to trustedMembers when verifiedRecord is unavailable. */ }
    return Promise.all(serverDevices.filter((d) => this.trustedMembers.has(d.id)).map((d) => this.unpackDevice(d)));
  }

  private async unpackDevice(d: Device): Promise<PlainDevice> {
    let meta: DeviceMeta | undefined, metaRev = 0n, metaBroken = false;
    if (d.meta?.securityHeader.length) {
      try { ({ msg: meta, header: { seq: metaRev } } = await this.openSecureMessage(d.meta, { kind: PayloadKind.DEVICE_META, requestId: d.id }, DeviceMetaSchema)); }
      catch (e) { if (!(e instanceof SecurityError) || e.code !== "unavailable") metaBroken = true; this.noteClock(e); }
    }
    return create(PlainDeviceSchema, { deviceId: d.id, kind: d.kind, createdMs: d.createdMs, lastSeenMs: d.lastSeenMs, meta, metaRev, metaBroken });
  }

  private async unpackAccountPrefs(o: AccountPrefsObject | undefined, refs?: string[], fromCache = false): Promise<PlainAccountPrefs> {
    if (!o || !o.envelope || o.revision === 0n) return create(PlainAccountPrefsSchema, { revision: 0n, prefs: emptyPrefs() });
    const cacheRef = !fromCache ? this.stashCacheObject({ kind: "prefs", raw: o }, refs) : "";
    try {
      const { msg, header } = await this.openSecureMessage(o.envelope, { kind: PayloadKind.ACCOUNT_PREFS, requestId: PREFS_OBJECT, seq: o.revision }, ProjectPrefsSchema);
      if (msg.schema !== 1 || header.seq !== o.revision) return create(PlainAccountPrefsSchema, { revision: o.revision, broken: true, cacheRef });
      return create(PlainAccountPrefsSchema, { revision: o.revision, prefs: msg, cacheRef });
    } catch (e) {
      this.noteClock(e);
      return create(PlainAccountPrefsSchema, { revision: o.revision, broken: true, cacheRef });
    }
  }

  private async unpackEvents(sessionId: string, stored: Array<{ seq: bigint; envelope?: Envelope }>, history: boolean, refs?: string[]): Promise<PlainEvent[]> {
    const machineId = this.sessions.get(sessionId)?.machineId ?? "";
    const out: PlainEvent[] = [];
    const historical = new Map<(typeof stored)[number], HistoryResult>();
    if (history) {
      const rows = stored.filter((se) => { try { return !!se.envelope && fromBinary(HeaderSchema, se.envelope.header).version === 3; } catch { return false; } });
      if (rows.length) {
        try {
          const results = await this.security.openHistoryBatch(rows.map((se) => ({ envelope: se.envelope!, seed: this.sessionSeeds.get(sessionId) })));
          rows.forEach((se, i) => historical.set(se, results[i]));
        } catch (reason) { rows.forEach((se) => historical.set(se, { status: "rejected", reason })); }
      }
    }
    const cacheEventRef = (se: (typeof stored)[number]) => se.envelope && refs ? this.stashCacheObject({ kind: "event", sessionId, raw: { seq: se.seq, envelope: se.envelope } }, refs) : "";
    for (const se of stored) {
      const opened = historical.get(se);
      historical.delete(se);
      try {
        const { msg, size } = await this.openSecureMessage(se.envelope, { kind: PayloadKind.EVENT, machineId, sessionId, seq: se.seq }, EventSchema, history, opened);
        if (msg.seq !== se.seq || msg.sessionId !== sessionId) throw new Error(l10n.t("errors.the_event_s_position_was_changed"));
        const verified = fromBinary(HeaderSchema, se.envelope!.header).version === 3;
        out.push(create(PlainEventSchema, { seq: se.seq, event: msg, verified, approxBytes: BigInt(Math.max(size, 64)), cacheRef: cacheEventRef(se) }));
      } catch (e) {
        if (e instanceof RecoveryHistoryUnavailable) {
          out.push(create(PlainEventSchema, { seq: se.seq, unavailable: true, approxBytes: 64n, cacheRef: cacheEventRef(se) }));
          continue;
        }
        out.push(create(PlainEventSchema, { seq: se.seq, broken: true, retired: e instanceof UnsupportedHistoryError }));
        this.noteClock(e);
        break;
      }
    }
    return out;
  }

  // --- Security Helpers ---

  private projectHeader(h: SecurityHeader) {
    return create(HeaderSchema, { version: 3, kind: h.kind, machineId: h.machine, sessionId: h.session, requestId: h.request, seq: h.seq, deadlineMs: h.deadline, sender: h.sender, compression: h.compression });
  }

  private async historicalPlace(want: Parameters<typeof expect>[1], header: ReturnType<Remote["projectHeader"]>, history: boolean) {
    if (history && [PayloadKind.EVENT, PayloadKind.IMAGE].includes(want.kind) && header.machineId !== want.machineId) {
      const replacements = this.sessionReplacements.get(want.sessionId ?? "") ?? this.sessions.get(want.sessionId ?? "")?.info?.machineReplacements ?? [];
      if ((await machineReplacementOrigins(await this.security.verifiedRecord(), want.machineId ?? "", replacements)).has(header.machineId)) {
        return { ...want, machineId: header.machineId };
      }
    }
    return want;
  }

  private async openSecureMessage<D extends DescMessage>(env: Envelope | undefined, want: Parameters<typeof expect>[1], schema: D, history = false, historical?: HistoryResult) {
    if (!env) throw new Error(l10n.t("errors.security_envelope_missing"));
    if ([PayloadKind.ACCOUNT_PREFS, PayloadKind.DEVICE_META, PayloadKind.MACHINE_META, PayloadKind.SESSION_META, PayloadKind.IMAGE].includes(want.kind)) history = true;
    const seed = [PayloadKind.EVENT, PayloadKind.IMAGE, PayloadKind.TERMINAL_OUTPUT, PayloadKind.TERMINAL_INPUT].includes(want.kind) ? this.sessionSeeds.get(want.sessionId ?? "") : undefined;
    rejectRetiredHistory(env);
    if (!history) await this.ensureWakeSecurity();
    let out: Awaited<ReturnType<DeviceSecurityClient["openHistory"]>>;
    try {
      out = historical?.status === "rejected" ? (() => { throw historical.reason; })()
        : historical?.status === "fulfilled" ? historical.value
          : history ? await this.security.openHistory(env, seed) : await this.security.openCurrent(env, seed);
    } catch (e) {
      if (e instanceof RecoveryHistoryUnavailable) expect(this.projectHeader(e.header), await this.historicalPlace(want, this.projectHeader(e.header), history));
      throw e;
    }
    const header = this.projectHeader(out.header);
    expect(header, await this.historicalPlace(want, header, history));
    try {
      const msg = fromBinary(schema, out.plaintext);
      validateSecurityBody(out.header, schema.typeName, msg);
      return { header, msg, size: out.plaintext.length };
    } catch { throw new Error(l10n.t("errors.security_body_type_or_direction_mismatch")); }
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
    h: Omit<Partial<import("./gen/remoteagent/v1/envelope_pb").Header>, "$typeName">,
    schema: D, msg: MessageShape<D>, beforeSign?: () => void, preparedBytes?: Uint8Array<ArrayBuffer>,
  ) {
    await this.ensureWakeSecurity();
    if (h.kind === PayloadKind.COMMAND) await this.checkNetworkSecurity();
    let purpose = Number(h.kind) as Purpose;
    if (h.kind === PayloadKind.TERMINAL_INPUT) {
      const f = msg as unknown as { kind: { case?: string } };
      if (f.kind.case === "resize") purpose = Purpose.TerminalResize;
      else if (f.kind.case === "historyRequest") purpose = Purpose.TerminalHistory;
    }
    const seed = purpose === Purpose.TerminalInput || purpose === Purpose.TerminalResize || purpose === Purpose.TerminalHistory ? this.sessionSeeds.get(h.sessionId ?? "") : undefined;
    const plain = preparedBytes ?? (toBinary(schema, msg) as Uint8Array<ArrayBuffer>);
    return this.security.seal({
      kind: h.kind ?? PayloadKind.UNSPECIFIED, purpose, machine: h.machineId ?? "", session: h.sessionId ?? "", request: h.requestId ?? "",
      seq: h.seq ?? 0n, deadline: h.deadlineMs ?? (h.kind === PayloadKind.TERMINAL_INPUT ? BigInt(Date.now() + 60000) : 0n), compression: 0,
    }, plain, seed, beforeSign);
  }

  private roundBudget(): number { return this.everOnline || this.networkGeneration > 0 ? 3000 : SETUP_IDLE_MS; }

  private retireNetworkIO() {
    this.direct.invalidate();
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
        const res = await auth.login({ protocolVersion: PROTOCOL_VERSION, deviceId: this.account.deviceId, challenge: ch.challenge, signature: await signLogin(this.account.devicePrivate, ch.challenge) }, { signal, timeoutMs });
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

  private syncDirectStatuses() {
    for (const id of this.machines.keys()) {
      const ready = this.direct.ready(id, { kind: "read" });
      if (this.directReadyCache.get(id) !== ready) { this.directReadyCache.set(id, ready); this.hostBridge?.pushRouteStatus(id, ready); }
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

  private directHint(machineId: string, cmd: CommandKind): RouteHint | undefined {
    if (cmd?.case === "getSharedFileChunk") return { kind: "bulk", file: `${cmd.value.sessionId}/${cmd.value.fileId}` };
    if (cmd?.case === "putImageChunk") {
      const upload = this.directTransfers.get(`${machineId}/${cmd.value.transferId}`);
      return upload ? { kind: "bulk", file: cmd.value.transferId!, upload, bytes: cmd.value.data?.length } : undefined;
    }
    return cmd?.case !== "getImageUploadStatus" && readOnlyCommand(cmd) ? { kind: "read" } : undefined;
  }

  fileUploadIssue(machineId: string): string { return this.peer(machineId).capabilities?.fileUpload === true ? "" : fileUploadUpdateRequired(); }

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
    const plain = saved ? new Uint8Array(saved.plain) : (toBinary(CommandSchema, create(CommandSchema, { kind: cmd, protocolVersion: PROTOCOL_VERSION })) as Uint8Array<ArrayBuffer>);
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
    if (cmd?.case === "resolveApproval" || cmd?.case === "answerQuestion" || cmd?.case === "answerPlan") {
      const action = cmd;
      const shown = this.liveSnapshots.get(sid);
      if (!shown) throw new Error(l10n.t("errors.this_request_has_not_been_verified_by_the_computer"));
      const key = action.case === "resolveApproval" ? action.value.approvalId : action.case === "answerQuestion" ? action.value.questionId : "";
      const findAction = (snap: LiveSnapshot) => snap.actions.find((e) => action.case === "resolveApproval" ? e.kind.case === "approvalRequested" && e.kind.value.approvalId === key : e.kind.case === "questionAsked" && e.kind.value.questionId === key);
      const displayed = findAction(shown);
      const observed = action.case === "answerPlan" ? shown.planEventSeq : (displayed?.seq ?? 0n);
      if (observed === 0n) throw new Error(l10n.t("errors.this_request_can_no_longer_be_acted_on"));
      await this.refreshLiveSnapshot(sid);
      const fresh = this.liveSnapshots.get(sid);
      if (!fresh) throw new Error(l10n.t("errors.the_current_request_could_not_be_verified_again"));
      if (action.case === "answerPlan") {
        if (fresh.planEventSeq !== observed || !equalBytes(fresh.planScreenHash, shown.planScreenHash)) throw new Error(l10n.t("errors.the_plan_display_changed_review_it_again"));
        cmd = { case: "answerPlan", value: { ...action.value, observedEventSeq: fresh.planEventSeq, observedScreenHash: fresh.planScreenHash } };
      } else {
        const current = findAction(fresh);
        if (current?.seq !== observed || !displayed || !equalBytes(toBinary(EventSchema, current), toBinary(EventSchema, displayed))) {
          throw new Error(l10n.t("errors.the_request_on_the_computer_changed_review_it_again"));
        }
        cmd = action.case === "resolveApproval" ? { case: "resolveApproval", value: { ...action.value, observedEventSeq: observed } } : { case: "answerQuestion", value: { ...action.value, observedEventSeq: observed } };
      }
    }
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
    if (theirs !== undefined && theirs !== PROTOCOL_VERSION) {
      throw new CommandError("failed_precondition", theirs < PROTOCOL_VERSION ? l10n.t("errors.remote_agent_on_is_out_of_date_update_it", { value: String(machineName(m)) }) : l10n.t("errors.this_page_is_out_of_date_refresh_or_update"), false, false, slot.evidence.unresolved ? "unknown" : "not_sent");
    }
    this.checkFileCapability(machineId, cmd, slot.evidence.unresolved);
    await slot.beforeDispatch?.(slot);

    const routeHint = this.directHint(machineId, cmd);
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
      const { outcome: ev, msg, directLegFailed } = await this.executeSealedAttempt({
        requestId, attemptId: newRequestID(), machineId, sessionId: sid, deadlineMs: deadline, commandBytes: plain,
        routeHint, forceRelay, priorUnknown,
        isScheduleList: cmd?.case === "schedule" && cmd.value.operation === "list",
        hasFiles: (cmd?.case === "send" || cmd?.case === "createSession") && !!cmd.value.fileIds?.length,
        signal, onHandoff: () => slot.evidence.trackHandoff(),
      });
      if (directLegFailed) forceRelay = true;

      if (ev.source === OutcomeSource.DAEMON) {
        const category = (ev.outerOutcome || msg?.outcome || "unknown") as Outcome;
        if (!ev.signed || !ev.hashesMatch || !ev.attemptBound || !ev.outerMatchesInner || !msg || category === "unknown") {
          slot.evidence.includePrior(true);
          throw new CommandError("unknown_outcome", l10n.t("errors.the_computer_s_reply_could_not_be_verified_command"), true, false, "unknown");
        }
        if ((category === "executed" && !msg.resultAvailable) || (category === "conflict" && msg.conflictKind === "identity")) {
          this.retryList();
          if (sid) void this.refreshLiveSnapshot(sid);
          throw new CommandError(msg.errorCode, l10n.t(category === "executed" ? "errors.executed_result_unavailable" : "errors.request_identity_conflict"), true, false, category, msg.conflictKind, msg.resultAvailable);
        }
        if (category !== "executed" || msg.errorCode) throw new CommandError(msg.errorCode, msg.errorMessage, true, readOnlyCommand(cmd), category, msg.conflictKind, msg.resultAvailable);
        return msg;
      }

      if (!ev.notForwarded) slot.evidence.includePrior(true);
      if (ev.routeWasDirect && routeHint) {
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

  get sharedFiles(): SharedFileManager {
    return (this.sharedFilesState ??= new SharedFileManager({
      getChunk: (sid, fileId, offset) => this.getSharedFileChunk(sid, fileId, offset),
      isConnected: (sid) => this.peer(this.sessions.get(sid)?.machineId).ready,
      isMachineOnline: (sid) => {
        const s = this.sessions.get(sid);
        return !!(s && this.peer(s.machineId).reachable);
      },
      isSessionEnded: (sid) => {
        const s = this.sessions.get(sid);
        return s?.info?.status === SessionStatus.EXITED || (s?.endedMs ?? 0n) > 0n;
      },
      sharedFileChunkBytes: (sid) => {
        const s = this.sessions.get(sid);
        return (s && this.peer(s.machineId).capabilities?.sharedFileChunkBytes) || 0;
      },
      store: sharedFileStore,
    }));
  }

  hasSharedFilesCapability(sid: string): boolean {
    const s = this.sessions.get(sid);
    return !!(s && this.peer(s.machineId).capabilities?.sharedFiles === true);
  }

  private get scheduleSeen() {
    return (this.scheduleSeenState ??= new ScheduleSeen(
      [this.account.server, this.account.deviceId],
      { getItem: (k) => localStorage.getItem(k), setItem: (k, v) => localStorage.setItem(k, v) },
      (run) => { void closeScheduleNotification(run); },
    ));
  }

  private emitScheduleHints() {
    const hints = [...this.sessions.keys()].map((sid) => {
      const tasks = this.sessionTasks(sid);
      return {
        sessionId: sid,
        scheduleName: tasks.length > 0 ? frequency(tasks[0].task.spec.trigger) : "",
        unread: tasks.some((e) => this.scheduleUnread(e.machineId, e.task)),
      };
    });
    this.hostBridge?.pushScheduleHints(hints);
  }

  scheduleBadge() { return this.scheduleSeen.count(this.taskEntries()); }
  scheduleUnread(machineId: string, task: ScheduleTask) { return this.scheduleSeen.unread(machineId, task); }
  scheduleHasNew(machineId: string, task: ScheduleTask) { return this.scheduleSeen.hasNew(machineId, task); }
  scheduleNewResults() { return this.taskEntries().some((e) => this.scheduleHasNew(e.machineId, e.task)); }
  markScheduleRead(machineId: string, task: ScheduleTask) { this.scheduleSeen.markRead(machineId, task); this.emitScheduleHints(); this.changed(); }
  markScheduleUnread(machineId: string, task: ScheduleTask, leaving = false) {
    const latest = this.schedules.get(machineId)?.tasks.find((current) => current.id === task.id);
    if (!latest) return;
    this.scheduleSeen.markUnread(machineId, latest, leaving);
    this.emitScheduleHints();
    this.changed();
  }
  setScheduleDetail(machineId?: string, taskId?: string, visit?: ScheduleDetailVisit) {
    const unread = this.scheduleSeen.setDetail(machineId, taskId, visit);
    this.emitScheduleHints();
    this.changed();
    return unread;
  }
  setScheduleSession(sessionId?: string) { this.scheduleSeen.setSession(sessionId); this.emitScheduleHints(); this.changed(); }
  setSchedulePage(page: string) { this.scheduleSeen.setPage(page); }
  setScheduleVisible(visible: boolean) { this.scheduleSeen.setVisible(visible); this.emitScheduleHints(); this.changed(); }
  hasLiveSchedules(machineId: string): boolean { return this.scheduleLive.has(machineId); }
  supportsSchedules(id: string): boolean {
    return this.peer(id).capabilities?.scheduledTasks === true && !(this.archiveMachines.has(id) && !this.trustedMembers.has(id));
  }
  taskEntries(): TaskEntry[] {
    return [...this.schedules].flatMap(([machineId, state]) =>
      this.supportsSchedules(machineId)
        ? state.tasks.filter(taskVisible).map((task) => ({ machineId, task, group: taskGroup(task) }))
        : [],
    );
  }
  sessionTasks(sid: string): TaskEntry[] {
    return this.taskEntries().filter((e) => e.task.spec.target.type === "resume_session" && e.task.spec.target.session === sid);
  }

  syncSchedules() {
    const readyList = this.listed || !!this.syncStateView?.machinesListed || this.machines.size > 0;
    for (const id of this.schedules.keys()) {
      if ((readyList && !this.machines.has(id)) || (this.archiveMachines.has(id) && !this.trustedMembers.has(id))) this.forgetSchedules(id);
    }
    for (const id of this.scheduleRequests.keys()) {
      if (!this.peer(id).available || !this.supportsSchedules(id)) this.scheduleRequests.delete(id);
    }
    for (const [id, m] of this.machines) {
      if (!this.peer(id).available || !this.supportsSchedules(id)) continue;
      const key = String(m.meta!.scheduleRevision);
      if (this.scheduleRequests.get(id) === key) continue;
      this.scheduleRequests.set(id, key);
      void this.refreshSchedules(id);
    }
  }

  private forgetSchedules(id: string) {
    this.scheduleSeen.forget(id);
    this.scheduleLive.delete(id);
    this.schedules.delete(id);
    this.scheduleRequests.delete(id);
    this.scheduleAgain.delete(id);
    void this.keepValue("schedules:" + id, undefined);
  }

  private async restoreScheduleSnapshot(id: string, kept: Record<string, unknown>, supportedOverride?: boolean) {
    const { msg } = await this.openSecureMessage(
      fromBinary(EnvelopeSchema, kept.bytes as Uint8Array),
      { kind: PayloadKind.COMMAND_RESULT, machineId: id, sessionId: "", requestId: kept.requestId as string },
      CommandResultSchema,
      true,
    );
    const supported = supportedOverride ?? this.supportsSchedules(id);
    if (!supported || this.removed || this.scheduleLive.has(id)) return;
    if (msg.kind.case === "schedule") {
      const parsed = JSON.parse(new TextDecoder().decode(msg.kind.value.resultJson)) as { tasks: ScheduleTask[] };
      if (Array.isArray(parsed.tasks)) {
        this.scheduleSeen.observe(id, parsed.tasks, { prune: false, live: false });
        this.schedules.set(id, { tasks: parsed.tasks, revision: 0n, loading: false, error: "" });
        this.emitScheduleHints();
      }
    }
  }

  private async keepScheduleSnapshot(id: string, value: Record<string, unknown>) {
    await this.keepValue("schedules:" + id, value);
    if (!this.machines.has(id) || !this.supportsSchedules(id) || this.removed) {
      await this.keepValue("schedules:" + id, undefined);
    }
  }

  refreshSchedules(id: string): Promise<void> {
    if (this.scheduleLoading.has(id)) {
      this.scheduleAgain.add(id);
      return this.scheduleLoading.get(id)!;
    }
    if (!this.peer(id).available || !this.supportsSchedules(id)) return Promise.resolve();
    const work = (async () => {
      do {
        this.scheduleAgain.delete(id);
        const revision = this.machines.get(id)?.meta?.scheduleRevision ?? 0n;
        const prev = this.schedules.get(id);
        this.schedules.set(id, { tasks: prev?.tasks ?? [], revision: prev?.revision ?? 0n, loading: true, error: "" });
        this.changed();
        try {
          const r = await this.machineCommand(id, { case: "schedule", value: { operation: "list" } });
          if (r.kind.case !== "schedule") throw new Error(l10n.t("errors.the_computer_did_not_return_a_task_list"));
          const parsed = JSON.parse(new TextDecoder().decode(r.kind.value.resultJson)) as { tasks: ScheduleTask[] };
          const tasks = parsed.tasks;
          if (!Array.isArray(tasks)) {
            throw new Error(l10n.t("errors.the_task_list_returned_by_the_computer_could_not"));
          }
          if (!this.machines.has(id) || !this.supportsSchedules(id) || this.removed) return;
          this.scheduleLive.add(id);
          this.scheduleSeen.observe(id, tasks, { live: true });
          this.schedules.set(id, { tasks, revision, loading: false, error: "" });
          this.emitScheduleHints();
          this.changed();
          void Promise.allSettled(
            [
              ...new Set(
                tasks
                  .filter((t) => t.spec.target.type === "resume_session")
                  .map((t) => t.spec.target.session)
                  .filter((sid): sid is string => !!sid && !this.sessions.get(sid)?.info),
              ),
            ].map((sid) => this.ensureSession(sid)),
          );
        } catch (e) {
          const state = this.schedules.get(id);
          if (!state || !this.supportsSchedules(id)) return;
          this.schedules.set(id, { ...state, loading: false, error: errorText(e) });
        }
        this.changed();
      } while (this.scheduleAgain.has(id) && this.peer(id).available);
    })().finally(async () => {
      this.scheduleLoading.delete(id);
      if (this.scheduleAgain.delete(id)) await this.refreshSchedules(id);
    });
    this.scheduleLoading.set(id, work);
    return work;
  }

  async changeSchedule(
    machineId: string,
    key: string,
    payload: { operation: string; taskId?: string; revision?: number; spec?: ScheduleSpec; requestId?: string; sessionId?: string },
  ): Promise<ScheduleTask> {
    if (!this.peer(machineId).available) {
      throw new Error(l10n.t("errors.the_computer_is_offline_connect_to_edit_tasks"));
    }
    if (!this.supportsSchedules(machineId)) {
      throw new Error(l10n.t("errors.update_remote_agent_on_this_computer_to_use_scheduled"));
    }
    const attemptKey = machineId + "/" + key;
    let attempt = this.scheduleAttempts.get(attemptKey);
    if (!attempt) {
      attempt = new Attempt();
      this.scheduleAttempts.set(attemptKey, attempt);
    }
    if (attempt.expired) {
      throw new Error(l10n.t("errors.the_previous_operation_s_outcome_is_still_unknown_reload"));
    }
    const req = attempt.next({
      operation: payload.operation,
      taskId: payload.taskId ?? "",
      revision: payload.revision ?? 0,
      spec: payload.spec,
      requestId: payload.requestId,
      sessionId: payload.sessionId,
    });
    try {
      const r = await this.machineCommand(
        machineId,
        {
          case: "schedule",
          value: {
            operation: req.payload.operation,
            taskId: req.payload.taskId,
            expectedRevision: BigInt(req.payload.revision),
            specJson: req.payload.spec ? new TextEncoder().encode(JSON.stringify(req.payload.spec)) : undefined,
            requestId: req.payload.requestId,
            sessionId: req.payload.sessionId,
          },
        },
        req.slot,
      );
      if (r.kind.case !== "schedule") throw new Error(l10n.t("errors.the_computer_did_not_return_a_task"));
      const task = JSON.parse(new TextDecoder().decode(r.kind.value.resultJson)) as ScheduleTask;
      attempt.settle(undefined, req);
      await this.refreshSchedules(machineId);
      return task;
    } catch (e) {
      attempt.settle(e, req);
      if (e instanceof CommandError && e.outcome === "conflict" && e.conflictKind === "business") {
        attempt.abandon();
        await this.refreshSchedules(machineId);
        throw new ScheduleRevisionError(l10n.t("errors.the_task_was_just_changed_updated_to_the_latest"));
      }
      throw e;
    }
  }

  async openScheduleRun(machineId: string, run: import("./schedules").ScheduleRun) {
    if (
      !run.session_id ||
      (this.sessions.get(run.session_id)?.machineId && this.sessions.get(run.session_id)?.machineId !== machineId)
    ) {
      return;
    }
    this.scheduleFocus.set(run.session_id, {
      seq: BigInt(run.start_seq || run.after_seq) + 1n,
      runId: run.id,
    });
    await this.ensureSession(run.session_id);
    this.touch();
    location.hash = "/s/" + encodeURIComponent(run.session_id);
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
    if (tray && notice && tray.note === notice.message) {
      tray.note = "";
      this.touch();
    }
    this.localNotices.delete(key);
  }

  persistDraft(key: string): Promise<void> | undefined {
    const revision = (this.draftRevisions.get(key) ?? 0) + 1;
    this.draftRevisions.set(key, revision);
    const tray = this.trays.get(key);
    if (this.closed || this.forgottenSessions.has(key) || key.startsWith("schedule:") || !this.localDrafts || !tray) return;
    if (!this.draftLoaded.has(key)) {
      if (!this.draftLoads.has(key)) void this.restoreDraft(key, tray).catch(() => undefined);
      return;
    }
    if (this.outboxWriteFailed.has(key)) void this.keepOutbox(key).catch(() => undefined);
    const text = this.drafts.get(key) ?? "";
    const snapshot = { items: [...tray.items], original: tray.original };
    const save = (this.outboxSaves.get(key) ?? Promise.resolve()).then(() => {
      if (this.draftRevisions.get(key) === revision) return this.localDrafts!.save(key, text, snapshot);
    });
    void save.then(
      () => {
        if (this.draftRevisions.get(key) === revision) this.clearLocalNotice(key);
      },
      () => this.localNotice(key, l10n.t("errors.the_draft_could_not_be_saved_locally_keep_the")),
    );
    return save;
  }

  restoreDraft(key: string, tray: Tray): Promise<void> {
    if (!this.localDrafts || this.forgottenSessions.has(key) || key.startsWith("schedule:") || this.draftLoaded.has(key)) {
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
    if (!key.startsWith("new:")) {
      void this.restoreOutbox(key).catch(() =>
        this.localNotice(key, l10n.t("errors.queued_records_could_not_be_restored_you_can_still")),
      );
    }
    return job;
  }

  private restoreOutbox(sid: string): Promise<void> {
    if (!this.localDrafts) return Promise.resolve();
    const old = this.outboxLoads.get(sid);
    if (old) return old;
    const job = this.localDrafts
      .readOutbox(sid)
      .then((saved) => {
        if (this.closed || this.forgottenSessions.has(sid)) return;
        const current = this.outbox(sid);
        const ids = new Set(current.map((o) => o.sendId));
        this.outboxes.set(sid, [...saved.filter((o) => !ids.has(o.sendId)), ...current]);
        const evs = this.events(sid);
        if (evs.length) this.reconcileOutbox(sid, evs);
        this.syncOutboxPin(sid);
        this.touch();
      })
      .catch((error) => {
        this.outboxLoads.delete(sid);
        throw error;
      });
    this.outboxLoads.set(sid, job);
    return job;
  }

  private forgetLocalSession(sid: string) {
    this.forgottenSessions.add(sid);
    this.drafts.delete(sid);
    this.draftLoaded.delete(sid);
    this.draftLoads.delete(sid);
    this.draftRevisions.delete(sid);
    for (const o of this.outbox(sid)) this.forwardingOutbox.delete(o.sendId);
    this.outboxes.delete(sid);
    this.outboxLoads.delete(sid);
    this.outboxSaves.delete(sid);
    this.outboxWriteFailed.delete(sid);
    this.localNotices.delete(sid);
    this.trays.delete(sid);
    this.scroll.delete(sid);
    void this.localDrafts?.removeSession(sid).catch(() => undefined);
  }

  private keepOutbox(sid: string): Promise<void> {
    if (!this.localDrafts || this.closed || this.forgottenSessions.has(sid)) return Promise.resolve();
    const saved = this.restoreOutbox(sid).then(() => {
      if (!this.forgottenSessions.has(sid)) return this.localDrafts!.saveOutbox(sid, this.outbox(sid));
    });
    this.outboxSaves.set(sid, saved);
    void saved.then(
      () => {
        if (this.outboxSaves.get(sid) === saved) {
          this.outboxWriteFailed.delete(sid);
          if (this.localNotices.get(sid)?.kind === "outbox_save") this.clearLocalNotice(sid);
        }
      },
      () => {
        if (this.outboxSaves.get(sid) === saved) this.outboxWriteFailed.add(sid);
        const forwarding = this.outbox(sid).some((o) => this.forwardingOutbox.has(o.sendId));
        this.localNotice(
          sid,
          forwarding
            ? l10n.t("errors.queued_records_could_not_be_saved_locally_messages_will")
            : l10n.t("errors.the_draft_could_not_be_saved_locally_keep_the"),
          "outbox_save",
        );
      },
    );
    return saved;
  }

  private syncOutboxPin(sid: string) {
    const pinned = this.outbox(sid).some((o) => o.status !== "sent");
    this.hostBridge?.pushSessionPin(sid, pinned);
  }

  private localNotice(key: string, message: string, kind: "draft" | "outbox_save" = "draft") {
    const tray = this.trays.get(key);
    if (!this.closed && tray) {
      this.localNotices.set(key, { message, kind });
      tray.note = message;
      this.touch();
    }
    this.notices.set(key, message);
    this.changed();
  }
  clearNotice(sid: string) {
    if (this.notices.delete(sid)) this.changed();
  }
  approvalReason(approvalId: string): string { return this.approvalReasons.get(approvalId) ?? ""; }
  setApprovalReason(approvalId: string, text: string) {
    text ? this.approvalReasons.set(approvalId, text) : this.approvalReasons.delete(approvalId);
  }
  clearApprovalReason(approvalId: string) { this.approvalReasons.delete(approvalId); }
  answerState(id: string): AnswerState { return this.answers.get(id) ?? noAnswer; }
  patchAnswer(id: string, p: Partial<AnswerState>) {
    this.answers.set(id, { ...this.answerState(id), ...p });
    this.changed();
  }
  tray(key: string): Tray | undefined { return this.trays.get(key); }
  setTray(key: string, tray: Tray) { this.trays.set(key, tray); }
  outbox(sid: string): Outgoing[] { return this.outboxes.get(sid) ?? []; }

  private patchOut(sid: string, sendId: string, p: Partial<Outgoing>) {
    const list = this.outbox(sid);
    const i = list.findIndex((o) => o.sendId === sendId);
    if (i < 0) return;
    const next = list.slice();
    next[i] = { ...next[i], ...p };
    this.outboxes.set(sid, next);
    this.syncOutboxPin(sid);
    this.changed();
  }

  private reconcileOutbox(sid: string, events: Event[]) {
    const box = this.outbox(sid);
    if (!box.length) return;
    const seen = new Set<string>();
    for (const e of events) {
      if (e.kind.case === "userMessage" && e.kind.value.requestId) seen.add(e.kind.value.requestId);
    }
    const left = box.filter((o) => !seen.has(o.sendId));
    if (left.length !== box.length) {
      if (left.length) this.outboxes.set(sid, left);
      else this.outboxes.delete(sid);
      this.syncOutboxPin(sid);
      void this.keepOutbox(sid).catch(() => undefined);
    }
  }

  canRestoreAttachments(sid: string, requestId: string): boolean {
    const out = this.outbox(sid).find((o) => o.sendId === requestId);
    return (
      !!out &&
      !!(out.text || out.imageIds?.length || out.files?.length) &&
      (!out.imageIds?.length || (out.attachments?.files.length === out.imageIds.length && out.attachments.files.every((f) => f.size > 0))) &&
      (!out.files?.length || out.files.every((f) => !!f.file))
    );
  }

  send(
    sid: string,
    text: string,
    imageIds: string[] = [],
    thumbs: Blob[] = [],
    mode = SendMode.STEER,
    attachments?: Outgoing["attachments"],
    files?: Outgoing["files"],
    imageSizes?: Outgoing["imageSizes"],
  ): string {
    const o: Outgoing = {
      sendId: newRequestID(),
      ...newSlot(),
      text,
      imageIds,
      thumbs,
      imageSizes,
      mode,
      attachments,
      files,
      status: "sending",
    };
    this.outboxes.set(sid, [...this.outbox(sid), o]);
    this.syncOutboxPin(sid);
    this.changed();
    void this.keepOutbox(sid).catch(() => undefined);
    void this.deliver(sid, o);
    return o.sendId;
  }

  resend(
    sid: string,
    failedId: string,
    text: string,
    imageIds: string[] = [],
    thumbs: Blob[] = [],
    files?: Outgoing["files"],
  ) {
    if (this.replaced.has(failedId)) return;
    const previous = this.outbox(sid).find((o) => o.sendId === failedId);
    this.replaced.set(
      failedId,
      this.send(
        sid,
        text,
        imageIds,
        thumbs.length ? thumbs : previous?.thumbs,
        SendMode.STEER,
        previous?.attachments,
        files,
        previous?.imageSizes,
      ),
    );
  }

  recorded(sid: string, requestIds: string[]) {
    if (!requestIds.length) return;
    const ids = new Set(requestIds);
    const box = this.outbox(sid);
    if (!box.some((o) => ids.has(o.sendId))) return;
    const left = box.filter((o) => !ids.has(o.sendId));
    if (left.length) this.outboxes.set(sid, left);
    else this.outboxes.delete(sid);
    this.syncOutboxPin(sid);
    this.changed();
    void this.keepOutbox(sid).catch(() => undefined);
  }

  clearSent(sid: string, ids: Set<string>) {
    const box = this.outbox(sid);
    const left = box.filter((o) => !ids.has(o.sendId));
    if (left.length) this.outboxes.set(sid, left);
    else this.outboxes.delete(sid);
    this.syncOutboxPin(sid);
    this.changed();
    void this.keepOutbox(sid).catch(() => undefined);
  }

  retrySend(sid: string, sendId: string) {
    const o = this.outbox(sid).find((x) => x.sendId === sendId);
    if (o && o.status === "failed") {
      void this.keepOutbox(sid).catch(() => undefined);
      void this.deliver(sid, o);
    }
  }

  dismissSend(sid: string, sendId: string, restored = false) {
    const drop = () => {
      this.outboxes.set(sid, this.outbox(sid).filter((o) => o.sendId !== sendId));
      this.syncOutboxPin(sid);
      void this.keepOutbox(sid).catch(() => undefined);
      this.changed();
    };
    if (!restored || !this.localDrafts) { drop(); return; }
    const saved = this.persistDraft(sid);
    if (saved) void saved.then(drop).catch(() => this.localNotice(sid, l10n.t("errors.the_draft_could_not_be_saved_the_original_message")));
  }

  private async deliver(sid: string, o: Outgoing) {
    this.patchOut(sid, o.sendId, { status: "sending", error: undefined, outcomeNotice: false, missingImages: false });
    const sending = restoreSlot({ requestId: o.requestId, deadline: o.deadline, prepared: o.prepared }, o.unknown ?? false);
    try {
      sending.beforeDispatch = async (slot) => {
        this.patchOut(sid, o.sendId, { requestId: slot.requestId, deadline: slot.deadline, prepared: slot.prepared });
        await this.keepOutbox(sid);
        this.forwardingOutbox.add(o.sendId);
      };
      const slot = await this.sendMessage(sid, o.text, o.sendId, sending, o.unknown, o.imageIds, o.mode, o.files?.map((f) => f.fileId));
      this.patchOut(sid, o.sendId, { status: "sent", requestId: slot.requestId, deadline: slot.deadline, prepared: slot.prepared });
    } catch (e) {
      const final = e instanceof CommandError && e.fromDaemon && e.outcome !== "not_sent" && !uncertain(e);
      const still = stillUnknown(e, o.unknown ?? false) || (!!sending.evidence.unresolved && !final);
      const missingImages =
        !still &&
        e instanceof CommandError &&
        e.code === "not_found" &&
        (!!o.imageIds?.length || !!o.files?.length) &&
        e.message.includes("is not on this machine; upload it again");
      this.patchOut(sid, o.sendId, {
        status: "failed",
        error: missingImages ? (o.files?.length ? l10n.t("attachment.ios_missing") : l10n.t("attachment.ios_image_missing")) : errorText(e),
        missingImages,
        unknown: still,
        outcomeNotice: e instanceof CommandError && e.needsResultRefresh,
        ...(still ? {} : { ...newSlot(), prepared: undefined }),
      });
    } finally {
      this.forwardingOutbox.delete(o.sendId);
    }
  }

  async sendMessage(
    sid: string,
    text: string,
    sendId: string,
    slot: RequestSlot,
    wasUnknown = false,
    imageIds: string[] = [],
    mode = SendMode.STEER,
    fileIds: string[] = [],
  ): Promise<RequestSlot> {
    if (wasUnknown) slot.evidence.includePrior(true);
    const cmd: CommandKind = {
      case: "send",
      value: { sessionId: sid, text, requestId: sendId, imageIds, fileIds, mode },
    };
    try {
      await this.command(sid, cmd, slot);
      return slot;
    } catch (e) {
      if (wasUnknown && e instanceof CommandError && e.outcome === "not_sent") {
        throw new CommandError(e.code, e.message, e.fromDaemon, e.readOnly, "unknown", e.conflictKind, e.resultAvailable);
      }
      if (wasUnknown || (e instanceof CommandError && e.outcome !== "not_sent") || uncertain(e)) throw e;
      if (e instanceof CommandError && e.code === "expired") {
        const fresh = newSlot();
        await this.command(sid, cmd, fresh);
        return fresh;
      }
      throw e;
    }
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
          const env = await this.sealSecureMessage(
            { kind: PayloadKind.TERMINAL_INPUT, machineId: s.machineId, sessionId: sid, requestId: streamId, seq, sender: this.account.deviceId },
            TerminalFrameSchema,
            create(TerminalFrameSchema, frame),
          );
          if (!closed && ws.ready) ws.send(toBinary(EnvelopeSchema, env));
        });
      },
      close: () => finish(l10n.t("errors.terminal_connection_closed"), false),
    };
  }

  prepareImage: Prepare = () => Promise.resolve({ bytes: new Uint8Array(), format: "png", width: 0, height: 0, size: 0 });

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

  async getMachineStatus(machineId: string, signal?: AbortSignal) {
    const r = await this.forward(machineId, "", { case: "getMachineStatus", value: {} }, undefined, signal);
    if (r.kind.case !== "machineStatus") throw new Error(l10n.t("machine_status.unavailable"));
    return r.kind.value;
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
          if (r.kind.value.imageStorage) this.imageStorage.set(machineId, r.kind.value.imageStorage);
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

  async saveSettings(machineId: string, next: DaemonSettings, cleanImages = false): Promise<DaemonSettings | undefined> {
    const r = await this.machineCommand(machineId, { case: "updateSettings", value: { settings: next, cleanImages } });
    if (r.kind.case !== "updateSettings") throw new Error(l10n.t("new_session.unexpected_reply"));
    if (r.kind.value.imageStorage) this.imageStorage.set(machineId, r.kind.value.imageStorage);
    this.settingsCache.set(machineId, Promise.resolve(r.kind.value.settings));
    this.settingsRead.set(machineId, r.kind.value.settings);
    this.keepInfo();
    this.changed();
    return r.kind.value.settings;
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
      await this.security.sync();
      const res = await this.api.listDevices({});
      await this.maybeRepublishAndDescribe(res.devices);
      const plainDevices = await this.unpackMemberDevices(res.devices);
      const next = new Map<string, DeviceInfo>(
        plainDevices.map((pd) => [
          pd.deviceId,
          {
            id: pd.deviceId,
            kind: pd.kind,
            pairingIncomplete: pd.createdMs === 0n && !pd.meta,
            createdMs: pd.createdMs,
            lastSeenMs: pd.lastSeenMs,
            meta: pd.meta,
            metaRev: pd.metaRev,
          },
        ]),
      );
      this.cachedDeviceMap = next;
      this.devicesStatus = "ready";
      if (this.hostBridge) {
        this.hostBridge.dispatchEvent(
          create(CoreEventSchema, {
            event: {
              case: "devicesFetched",
              value: create(DevicesFetchedEventSchema, {
                opId: 0n,
                pushSeq: devPushSeq,
                devices: plainDevices,
              }),
            },
          }),
        );
      }
      this.changed();
    } catch {
      this.devicesStatus = "error";
      this.changed();
    }
  }

  async handoffCandidates(): Promise<HandoffOffer[]> {
    await this.loadDevices(false);
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
      const meta = await this.sealSecureMessage(
        { kind: PayloadKind.DEVICE_META, requestId: this.account.deviceId, seq: revision + 1n, sender: this.account.deviceId },
        DeviceMetaSchema,
        create(DeviceMetaSchema, { ...create(DeviceMetaSchema, own?.meta), handoffRecoveryEd: publicKey }),
        check,
      );
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
      const env = await this.sealSecureMessage(
        { kind: PayloadKind.DEVICE_META, requestId: id, seq: rev + 1n, sender: this.account.deviceId },
        DeviceMetaSchema,
        create(DeviceMetaSchema, change(cur?.meta)),
        check,
      );
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
    for (let attempt = 0; attempt < 5; attempt++) {
      const { object: o } = await api.getAccountPrefs({ objectId: PREFS_OBJECT });
      if (!o?.envelope || o.revision === 0n || parseHeader(new Uint8Array(o.envelope.securityHeader)).contentEpoch >= boundary) break;
      let snapshot;
      try {
        snapshot = await this.security.openMessage(o.envelope, ProjectPrefsSchema, true);
      } catch (e) {
        if (e instanceof SecurityError && e.code === "unavailable") break;
        throw e;
      }
      if (snapshot.header.purpose !== Purpose.Prefs || snapshot.header.seq !== o.revision || snapshot.header.request !== PREFS_OBJECT) {
        throw new SecurityError("untrusted");
      }
      const envelope = await this.security.seal(
        { kind: 12, purpose: Purpose.Prefs, machine: "", session: "", request: PREFS_OBJECT, seq: o.revision + 1n, deadline: 0n, compression: 0 },
        toBinary(ProjectPrefsSchema, snapshot.msg),
      );
      if ((await api.putAccountPrefs({ objectId: PREFS_OBJECT, expectedRevision: o.revision, envelope })).applied) break;
      if (attempt === 4) throw new Error(l10n.t("errors.the_recovery_object_was_updated_concurrently_retrying_on_the"));
    }
    for (let attempt = 0; attempt < 5; attempt++) {
      const own = (await api.listDevices({})).devices.find((d) => d.id === this.account.deviceId);
      if (!own?.meta || parseHeader(new Uint8Array(own.meta.securityHeader)).contentEpoch >= boundary) break;
      const snapshot = await this.security.openMessage(own.meta, DeviceMetaSchema, true);
      if (snapshot.header.purpose !== Purpose.DeviceMeta || snapshot.header.sender !== own.id || snapshot.header.seq !== own.metaRevision) {
        throw new SecurityError("untrusted");
      }
      const meta = await this.security.seal(
        { kind: 13, purpose: Purpose.DeviceMeta, machine: "", session: "", request: own.id, seq: own.metaRevision + 1n, deadline: 0n, compression: 0 },
        toBinary(DeviceMetaSchema, snapshot.msg),
      );
      if ((await api.putDeviceMeta({ deviceId: own.id, expectedRevision: own.metaRevision, meta })).applied) break;
      if (attempt === 4) throw new Error(l10n.t("errors.the_recovery_object_was_updated_concurrently_retrying_on_the"));
    }
  }
}

export function errorText(e: unknown): string {
  if (e instanceof RecoveryHistoryUnavailable) {
    return l10n.t("errors.history_keys_unavailable_records_from_before_recovery_cannot_be");
  }
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
