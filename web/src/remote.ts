import { readSecurityOperation } from "./security-operation";
import { AuthorityClient, handoffCandidates as verifiedHandoffCandidates, type HandoffOffer } from "./authority-client";
import type { AuthorityPairing } from "./authority-pairing";
import { clearLoginToken, loadLoginToken, saveLoginToken } from "./device-security-store";
import { machineReplacementOrigins } from "./machine-replacement";
import { PlanProposedSchema } from "./gen/remoteagent/v1/event_pb";
import {
  LiveSnapshotSchema,
  type LiveSnapshot,
} from "./gen/remoteagent/v1/remote_pb";
import type { SecurityRecord } from "./device-security-store";
import { derive, agentOf, type Derived } from "./model";
import { isSkeleton } from "./skeleton";
import {
  DeviceSecurityClient,
  RecoveryHistoryUnavailable,
  validateSecurityBody,
  type SecurityStorage,
  type HistoryResult,
} from "./device-security-client";
import {
  parseHeader,
  Purpose,
  SecurityError,
  stateHash,
  type Member,
  type SecurityHeader,
} from "./device-security";
import {
  PairApprovalSchema,
  RegisterDeviceRequestSchema,
} from "./gen/remoteagent/v1/relay_pb";
// Remote keeps the page in sync with the server: it logs in, holds the
// decrypted machines, sessions and events, follows the push stream, and
// sends sealed commands. Content is only ever decrypted here.

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
import { relayFetch } from "./transport";
import { restorePushPrefs, watchSecurityPush } from "./push";
import {
  checkServer,
  concat,
  deriveKeys,
  deviceID,
  importSessionKey,
  newRequestID,
  openMessage as openLegacyMessage,
  open as openLegacyEnvelope,
  signLogin,
  expect,
  type Keys,
} from "./crypto";
import {
  EnvelopeSchema,
  HeaderSchema,
  type Envelope,
  PayloadKind,
} from "./gen/remoteagent/v1/envelope_pb";
import {
  Agent,
  ImageFormat,
  InputStatus,
  SendMode,
  SessionStatus,
  type Event,
} from "./gen/remoteagent/v1/event_pb";
import { matchesFormat, mimeOf } from "./imageview";
import {
  DaemonSettingsSchema,
  ListModelsResponseSchema,
  SessionSchema,
  type ImageStorageUsage,
  type DaemonSettings,
  type ListModelsResponse,
  type Session,
} from "./gen/remoteagent/v1/local_pb";
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
  type AccountPrefsObject,
  AccountPrefsObjectSchema,
  type Device,
  type Machine as RemoteMachine,
  type RemoteSession,
  type SessionCursor,
  type StoredEvent,
} from "./gen/remoteagent/v1/relay_pb";
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

export type CommandKind = MessageInitShape<typeof CommandSchema>["kind"];
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
import { stillUnknown } from "./attempt";
import { describeBrowser, type DeviceInfo } from "./devices";
import type { Prepare, Tray } from "./attach";
import {
  commitOp,
  emptyPrefs,
  type PrefKind,
  type PrefOp,
  type PrefsSnapshot,
  type PrefsStore,
} from "./prefs";
import { endedUnsent, machineName, modelLabel, type Outgoing } from "./model";
import { eventCache, type CachedEvent, type EventCache } from "./eventcache";
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

export interface MachineView {
  id: string;
  online: boolean;
  lastSeenMs: bigint;
  meta?: MachineMeta;
  metaRev: bigint;
}

export interface SessionView {
  id: string;
  machineId: string;
  key: CryptoKey;
  info?: Session;
  checkpoint?: LiveSnapshot;
  checkpointStatus?: "ready" | "waiting" | "invalid";
  metaRev: bigint;
  lastSeq: bigint;
  updatedMs: bigint;
  // When the session ended as the server lists it, 0 while it has not:
  // its place in the paged listing. What is shown comes from info.
  endedMs: bigint;
}

/** How long a failed page or lookup waits before it is asked for again. */
const RETRY_MS = 5000;

/** Ended sessions fetched per page of the listing. */
export const SESSION_PAGE = 50;

interface EventBuffer {
  unavailableSeqs?: Set<bigint>;
  unverifiedLegacy?: boolean;
  verifiedSeqs?: Set<bigint>;
  verifiedEvents?: Event[];
  verifiedDerived?: { count: number; agent: Agent; value: Derived };
  events: Event[]; // contiguous from seq 1
  recent?: Event[]; // a contiguous suffix, shown alone until backfill joins it
  tailRequested?: boolean;
  pending: Map<bigint, Event>; // arrived ahead of a gap
  fetching: boolean;
  refetch: boolean;
  loaded: boolean;
  // The copy cached in this browser has been read, or there is none.
  restored: boolean;
  // The last fetch failed; a retry may help.
  error: string;
  // An event did not open; history stops before it.
  broken: boolean;
  inputStatuses?: Map<string, InputStatus>;
  bytes?: number;
  usedAt?: number;
}

/** How far a session's history got: loading, complete, a fetch that
 * failed, or an event that does not open. */
export interface History {
  backfilling?: boolean;
  unavailable?: number;
  loaded: boolean;
  error: string;
  broken: boolean;
}

/** A command's request ID and deadline. Sealing a retry with the same
 * pair makes the daemon return its first answer instead of running the
 * command again. */
export interface RequestSlot {
  requestId: string;
  deadline: number;
}

export function newSlot(now = Date.now()): RequestSlot {
  return { requestId: newRequestID(), deadline: now + COMMAND_TTL_MS };
}

export class CommandError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly fromDaemon = false,
  ) {
    super(message);
  }
}

/** Whether a command may or may not have run: a retry must then reuse
 * its request slot, so the daemon answers from its log rather than
 * running it again. A definite answer (done, refused, never sent) lets
 * the next attempt start afresh. */
export function uncertain(e: unknown): boolean {
  if (e instanceof CommandError)
    return e.code === "unknown" || e.code === "aborted";
  return e instanceof ConnectError;
}

/** The state of answering one question call. */
export interface AnswerState {
  index: number; // the question being answered
  ticked: number[]; // options ticked on a multi-select question, unsent
  // The last answer tried and its request slot; kept after an unknown
  // outcome so a retry is the same request.
  last?: { key: string; slot: RequestSlot };
  busy: string; // key of the answer in flight, kept after the last one succeeds
  error: string;
  unknown: boolean; // the last failure left the outcome unknown
}

const noAnswer: AnswerState = {
  index: 0,
  ticked: [],
  busy: "",
  error: "",
  unknown: false,
};

/** Where the API is reached: the account's server, or with
 * VITE_SAME_ORIGIN_API the page's own origin, which forwards to it. */
export function apiBase(server: string): string {
  const origin = checkServer(server);
  return import.meta.env.VITE_SAME_ORIGIN_API ? location.origin : origin;
}

export interface TermConn {
  send(frame: MessageInitShape<typeof TerminalFrameSchema>): void;
  close(): void;
}

export type ConnState = "connecting" | "online" | "offline";
/** Which stretch of the way to the computer is down, the nearest one only
 * (client-v30 「断线」): past it nothing is known. "waiting" is a connection
 * being made or just lost, which mostly comes back by itself. */
export type Link = "online" | "waiting" | "no-network" | "no-server";

export const COMMAND_TTL_MS = 60_000;

/** Largest piece of an image upload: each is sealed into one command,
 * which must stay under 1 MiB. */
const UPLOAD_CHUNK = 768 * 1024;

/** How many opened display copies stay in memory. */
const IMAGE_CACHE = 40;

/** A display copy fetched with GetImage: shown only when it opened where
 * it belongs and is the format it says. */
export type ImageResult =
  | {
      kind: "ok";
      unverifiedLegacy?: boolean;
      blob: Blob;
      width: number;
      height: number;
      format: ImageFormat;
    }
  | { kind: "expired" } // kept past its retention: only the preview is left
  | { kind: "missing" } // never stored
  | { kind: "bad" }; // did not open, or is not the format it claims

/** The account preferences object with the project list's settings. */
export const PREFS_OBJECT = "project-prefs";

export interface PrefsState {
  status: "loading" | "ready" | "error";
  error: string;
  snap?: PrefsSnapshot;
  // Operations in flight, by project path.
  pending: Map<string, PrefKind>;
  // The last operation that did not go through, to retry as it was.
  failed?: { op: PrefOp; error: string };
  // When another client last changed the list.
  changedAt?: number;
}

/** Registers this browser as a device of an existing account. The phone's
 * pairing approval, not the account root, authorizes the registration. */
export async function joinWithApproval(
  accountPublicKey: Uint8Array<ArrayBuffer>,
  server: string,
  enrollment: {
    device: CryptoKeyPair;
    pairingId: string;
    approval: Uint8Array;
    approvalSignature: Uint8Array;
  },
  existing?: StoredAccount,
  security?: import("./device-security-store").SecurityRecord,
  replace = false,
): Promise<StoredAccount> {
  server = checkServer(server);
  const account = await deviceID(accountPublicKey);
  const device = enrollment.device;
  const pub = new Uint8Array(
    await crypto.subtle.exportKey("raw", device.publicKey),
  );
  const a = fromBinary(PairApprovalSchema, enrollment.approval);
  const same = (x: Uint8Array, y: Uint8Array) =>
    x.length === y.length && x.every((v, i) => v === y[i]);
  if (
    a.accountId !== account ||
    a.pairingId !== enrollment.pairingId ||
    a.candidateKind !== DeviceKind.CLIENT ||
    a.replacesDeviceId !== "" ||
    !same(a.candidateDevicePublicKey, pub) ||
    a.nonce.length !== 16 ||
    a.expiresMs <= BigInt(Date.now()) ||
    a.approverPublicKey.length !== 32 ||
    (await deviceID(a.approverPublicKey as Uint8Array<ArrayBuffer>)) !==
      a.approverDeviceId ||
    a.approverDeviceId === (await deviceID(pub))
  )
    throw new Error("配对批准与这台设备不匹配或已过期");
  const signer = await crypto.subtle.importKey(
    "raw",
    a.approverPublicKey as Uint8Array<ArrayBuffer>,
    "Ed25519",
    false,
    ["verify"],
  );
  if (
    !(await crypto.subtle.verify(
      "Ed25519",
      signer,
      enrollment.approvalSignature as Uint8Array<ArrayBuffer>,
      concat(
        new TextEncoder().encode("remote-agent/v1/pair-approval\0"),
        enrollment.approval,
      ),
    ))
  )
    throw new Error("配对批准签名无效");
  const reg = toBinary(
    DeviceRegistrationSchema,
    create(DeviceRegistrationSchema, {
      accountPublicKey,
      devicePublicKey: pub,
      kind: DeviceKind.CLIENT,
      createdMs: BigInt(Date.now()),
    }),
  );
  const stored: StoredAccount = {
    server,
    root: existing?.root,
    rootRaw: existing?.rootRaw && new Uint8Array(existing.rootRaw),
    devicePrivate: device.privateKey,
    deviceId: await deviceID(pub),
  };
  const request = toBinary(
    RegisterDeviceRequestSchema,
    create(RegisterDeviceRequestSchema, {
      protocolVersion: PROTOCOL_VERSION,
      registration: reg,
      approval: enrollment.approval,
      approvalSignature: enrollment.approvalSignature,
    }),
  );
  await saveRegistration({ account: stored, request, security, replace });
  await resumeRegistration();
  return stored;
}

/** How long the stream may take from dialing to the server's ready. */
const OPEN_TIMEOUT_MS = 15_000;
const STREAM_IDLE_MS = 60_000;
// One default server heartbeat (20 s), plus 10 s for scheduling/network delay.
const STREAM_RESUME_IDLE_MS = 30_000;
const RECONNECT_NOTICE_MS = 1_500;
const RPC_TIMEOUT_MS = 45_000;
// Allow one full RPC plus local verification/storage. Progress starts a
// fresh budget, so several healthy round trips do not time out in aggregate.
const SETUP_IDLE_MS = RPC_TIMEOUT_MS + OPEN_TIMEOUT_MS;

/** What a plan the live snapshot says is pending reads as: the plan event
 * it names (planEventSeq), the clean Markdown the CLI proposed, when this
 * device holds that event readable (readable: opened and verified at that
 * place of the session's history, which binds it, so no other event can
 * stand in for it). Only without it does the terminal's text stand in, box
 * lines and all. Whether a plan is pending, and what an answer is bound to
 * (the event's seq and the screen's hash), stay the snapshot's alone. */
export function livePlanText(
  live: LiveSnapshot,
  events: readonly Event[],
  readable: (seq: bigint) => boolean,
): string {
  const seq = live.planEventSeq;
  // A whole history holds event n at n - 1; otherwise look for it.
  let e = seq > 0n && seq <= BigInt(events.length) ? events[Number(seq) - 1] : undefined;
  if (e?.seq !== seq)
    for (let i = events.length - 1; i >= 0 && !(e?.seq === seq); i--) e = events[i];
  if (
    e?.seq === seq &&
    e.kind.case === "planProposed" &&
    readable(seq) &&
    e.kind.value.text.trim() &&
    !isSkeleton(e.kind.value)
  )
    return e.kind.value.text;
  return live.planScreen;
}

export class Remote {
  private historicalDerivations = new WeakMap<Event[], { count: number; agent: Agent; value: Derived }>();
  private currentDerivations = new WeakMap<Derived, {
    sid: string; live?: LiveSnapshot; checkpoint?: LiveSnapshot; agent: Agent;
    events?: Event[]; count: number; value: Derived;
  }>();
  private liveSnapshots = new Map<string, LiveSnapshot>();
  private liveErrors = new Map<string, string>();
  private liveJobs = new Map<string, Promise<void>>();

  private directoryJob?: { head: Uint8Array; promise: Promise<void>; invalidated?: boolean };
  private directoryHead?: Uint8Array;
  private recoveryObjectsPending = false;
  private trustedMembers = new Map<string, Member>();
  private archiveMachines = new Set<string>();
  private descriptionPending = false;
  authorityDeviceId = "";
  /** Set when a state is from the future relative to this device's clock. */
  clockWarning = "";
  authorityError = "";

  private sessionSeeds = new Map<string, Uint8Array<ArrayBuffer>>();
  readonly security: DeviceSecurityClient;
  readonly authority: AuthorityClient;
  get isAuthority() { return this.authorityDeviceId === this.account.deviceId; }
  get recoveryPending() { return this.security.recoveryPending; }
  get recoveryReadyMs() { return this.security.recoveryReadyMs; }
  get recoveryStatusStale() { return this.security.recoveryStatusStale; }
  private unwatchSecurityPush?: () => void;
  /** Development fixture hooks; the real client never enables these. */
  demoFlag(_name: string): boolean {
    return false;
  }
  machines = new Map<string, MachineView>();
  sessions = new Map<string, SessionView>();
  conn: ConnState = "connecting";
  error = "";
  // Whether the stream has been up at least once, and how many attempts
  // failed since it last was: tells "connecting" from "reconnecting" and
  // from "cannot reach the server".
  everOnline = false;
  /** The server no longer knows this device: removed from the account,
   * or the server's data is gone. Nothing connects until it joins again. */
  removed = false;
  removalVerified = false;
  failures = 0;
  connError = "";
  // The browser's word on whether this device has a network at all; it
  // cannot tell a network that is up but leads nowhere.
  network = typeof navigator === "undefined" || navigator.onLine !== false;
  /** How long a lost connection is only being remade, before the server
   * counts as out of reach. */
  linkGraceMs = 10_000;
  private waited = false;
  private waitingNotice = false;
  private graceTimer?: ReturnType<typeof setTimeout>;
  private begun = false;
  // When the link to the server last went down, while it is down: what is
  // shown as running may have changed since, so clocks stop there.
  lostAt?: number;
  // Set once machines and sessions have been listed; listError says why
  // the last listing failed.
  listed = false;
  listError = "";
  // The listing of sessions comes a page at a time: every session that
  // has not ended, then the ended ones from the latest. moreSessions says
  // the server has ended sessions not fetched yet; loadingSessions that a
  // page is on its way. missing are sessions asked for by ID that do not
  // exist.
  moreSessions = false;
  loadingSessions = false;
  /** Whether there may be ended sessions that are not held: the server
   * has further pages, or has not been asked yet and what is held is
   * what this browser kept from its last visit. */
  get sessionsOpen(): boolean {
    return this.moreSessions || !this.listed;
  }
  missing = new Set<string>();
  private nextSessions?: SessionCursor;
  private listing = 0; // counts first pages, so a late page of an older listing is not continued from
  private paging?: Promise<boolean>;
  private pageRetryAt = 0; // a page that failed is not asked for again at once
  private relisting = 0; // first pages on their way
  private loadingAll = false;
  private asking = new Set<string>();
  private askRetryAt = new Map<string, number>();
  private listRetry = 2000;
  private listTimer?: ReturnType<typeof setTimeout>;
  // First messages of sessions started here, shown until the CLI takes
  // them; and the machine chosen for a project that has no session yet.
  prompts = new Map<string, string>();
  draftMachines = new Map<string, string>();
  // Sessions started here whose record has not arrived yet, and the
  // project whose new-session box should take focus when shown.
  created = new Set<string>();
  // Sessions started here with 只打开终端: they open on the terminal.
  termFirst = new Set<string>();
  // What the user has typed and not sent, by session ID or "new:" +
  // project path, just like the image trays. Sent messages leave the draft.
  drafts = new Map<string, string>();
  approvalReasons = new Map<string, string>();
  // Docked items the user closed with 收起 (Dock's keys): they stay one
  // line, while a new item comes open. One that came while a draft was in
  // the input starts here too.
  dockClosed = new Set<string>();
  // Docked items already shown, so that only a new one looks at the draft.
  dockSeen = new Set<string>();
  // The account's devices, machines and clients (ListDevices); names of
  // clients are sealed in their metadata.
  devices = new Map<string, DeviceInfo>();
  devicesStatus: "loading" | "ready" | "error" = "loading";
  // The account's project list preferences.
  prefs: PrefsState = { status: "loading", error: "", pending: new Map() };
  // Images chosen for each draft, by draft key (a session ID, or
  // "new:" + project path), and how a chosen file is prepared for sending.
  trays = new Map<string, Tray>();
  // The image codecs are fetched when the first image is chosen.
  prepareImage: Prepare = async (file, original) =>
    (await import("./sendimages")).prepareForSending(file, original);
  // Cancels asked for from this page, by the queued message's request ID,
  // until the message leaves the queue.
  cancels = new Map<string, { state: "canceling" | "failed"; error: string }>();
  resumes = new Map<string, { state: "sending" | "failed"; error: string }>();
  // Where each session's chat was scrolled to, so that leaving for the
  // terminal, the queue or a detail page and coming back keeps the place.
  scroll = new Map<string, { top: number; stick: boolean }>();
  // How each question call is being answered, by call ID: kept here so the
  // docked card and the question page share it.
  private answers = new Map<string, AnswerState>();

  answerState(id: string): AnswerState {
    return this.answers.get(id) ?? noAnswer;
  }

  patchAnswer(id: string, patch: Partial<AnswerState>) {
    this.answers.set(id, { ...this.answerState(id), ...patch });
    this.changed();
  }
  focusProject = "";

  private buffers = new Map<string, EventBuffer>();
  private historyUsers = new Map<string, number>();
  private historyTrim?: ReturnType<typeof setTimeout>;

  /** A mounted consumer pins its complete history, including pending sends. */
  retainHistory(sid: string): () => void {
    this.historyUsers.set(sid, (this.historyUsers.get(sid) ?? 0) + 1);
    this.events(sid);
    let released = false;
    return () => {
      if (released) return;
      released = true;
      const n = (this.historyUsers.get(sid) ?? 1) - 1;
      if (n) this.historyUsers.set(sid, n);
      else this.historyUsers.delete(sid);
      const b = this.buffers.get(sid);
      if (b) b.usedAt = Date.now();
      this.trimHistory();
    };
  }

  private trimHistory() {
    clearTimeout(this.historyTrim);
    const idle = [...this.buffers]
      .filter(
        ([sid, b]) =>
          !this.historyUsers.has(sid) &&
          !this.outbox(sid).some((o) => {
            const status = b.inputStatuses?.get(o.sendId);
            const finished =
              this.sessions.get(sid)?.info?.status === SessionStatus.EXITED ||
              (status !== undefined &&
                [
                  InputStatus.CANCELED,
                  InputStatus.FAILED,
                  InputStatus.NEEDS_TERMINAL,
                  InputStatus.UNCONFIRMED,
                ].includes(status));
            return (
              o.status === "sending" ||
              (o.status === "sent" && !!o.thumbs?.length && !finished)
            );
          }),
      )
      .sort((a, b) => (a[1].usedAt ?? 0) - (b[1].usedAt ?? 0));
    let bytes = idle.reduce((n, [, b]) => n + (b.bytes ?? 0), 0);
    let count = idle.length;
    const now = Date.now();
    for (const [sid, b] of idle) {
      if (
        !this.sessions.has(sid) ||
        count > 8 ||
        bytes > 16 * 1024 * 1024 ||
        now - (b.usedAt ?? now) >= 60_000
      ) {
        this.buffers.delete(sid);
        count--;
        bytes -= b.bytes ?? 0;
      }
    }
    // Deferred trimming also catches histories read once without a mounted consumer.
    if (!this.closed && this.buffers.size)
      this.historyTrim = setTimeout(() => this.trimHistory(), 60_000);
  }
  // Pushes seen per machine, session and machine removal, so that a list
  // fetched before a push cannot undo it.
  private machinePushes = new Map<string, number>();
  private sessionPushes = new Map<string, number>();
  private removedPushes = new Map<string, number>();
  private token = "";
  private tokenExpires = 0;
  private loggingIn?: Promise<void>;
  private tokenWrites: Promise<void> = Promise.resolve();
  private discardToken() {
    this.token = "";
    this.tokenExpires = 0;
    this.tokenWrites = this.tokenWrites.then(() => clearLoginToken(this.account.server, this.account.deviceId)).catch(() => undefined);
  }
  private api: Client<typeof ClientService>;
  private ws?: WebSocket;
  private retry = 1000;
  private closed = false;
  private reconcilingSecurity = 0;
  private generation = 0;
  private retryTimer?: ReturnType<typeof setTimeout>;
  private securityTimer?: ReturnType<typeof setInterval>;
  private openTimer?: ReturnType<typeof setTimeout>;
  private idleTimer?: ReturnType<typeof setTimeout>;
  private setupProgress?: () => void;
  private terminals = new Map<WebSocket, (reason: string) => void>();
  private listeners = new Set<() => void>();
  private version = 0;

  protected constructor(
    readonly account: StoredAccount,
    private legacyKeys: Keys | undefined,
    private cache?: EventCache,
    securityStorage?: SecurityStorage,
    protected readonly historyPageSize = 500,
    protected readonly sessionPageSize = SESSION_PAGE,
  ) {
    const auth: Interceptor = (next) => (req) => {
      req.header.set("Authorization", "Bearer " + this.token);
      return next(req).catch((e: unknown) => {
        if (e instanceof ConnectError && e.code === Code.Unauthenticated)
          this.discardToken();
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
      async (after, limit) => {
        this.setupProgress?.();
        const reply = await this.api.getSecurityStates({
          afterEpoch: after,
          limit,
        });
        this.setupProgress?.();
        return {
          states: reply.states.map((s) => ({
            state: s.state,
            recovery: s.recovery,
          })),
          headEpoch: reply.headEpoch,
          lease: reply.lease,
          recoveryPending: reply.recoveryPending,
          recoveryReadyMs: reply.recoveryReadyMs,
        };
      },
      () => !this.closed || this.reconcilingSecurity > 0,
      securityStorage,
    );
    const pairing = createClient(PairingService, createConnectTransport({
      fetch: relayFetch, baseUrl: apiBase(account.server), useBinaryFormat: true,
      defaultTimeoutMs: RPC_TIMEOUT_MS, interceptors: [auth],
    }));
    this.authority = new AuthorityClient(account, this.security, this.api, pairing,
      (publicKey, check, persist, request) => this.publishHandoffOffer(publicKey, check, persist, request));
    this.startPairing = async (web, replacesDeviceId = "") =>
      (await import("./authority-pairing")).AuthorityPairing.start(this.authority, pairing, web, replacesDeviceId);
  }
  readonly startPairing: (web: string, replacesDeviceId?: string) => Promise<AuthorityPairing>;
  async handoffCandidates(): Promise<HandoffOffer[]> {
    await this.loadDevices(false);
    return verifiedHandoffCandidates(await this.security.verifiedRecord(), [...this.devices.values()].flatMap((d) =>
      d.meta?.handoffRecoveryEd.length === 32 ? [{ id: d.id, recoveryEd: new Uint8Array(d.meta.handoffRecoveryEd) }] : []));
  }
  async handoffAuthority(incoming: string) {
    try {
      await this.authority.handoffAuthority(incoming, await this.handoffCandidates());
    } finally {
      await this.checkLocalRemoval();
    }
  }
  private async checkLocalRemoval() {
    // A refused login alone proves nothing. Only the pinned, verified chain
    // can distinguish removal from a server that lost its credentials.
    this.reconcilingSecurity++;
    try {
      if (!(await this.security.verifiedRecord()).member) {
        this.removalVerified = true;
        this.removed = true;
        this.stop();
      }
    } catch { /* Keep the unverified notice and all completion material. */ }
    finally { this.reconcilingSecurity--; }
  }
  async resumeSecurityOperation() {
    // A handoff may stop the transport before its outcome is reconciled.
    // Explicit foreground reconciliation must still use the original lock,
    // request and visibility checks, without restarting that transport.
    const stopped = this.closed;
    if (stopped) this.authority.lifecycle.resume();
    this.reconcilingSecurity++;
    try {
      await this.authority.resumeSecurityOperation();
      await this.checkLocalRemoval();
    } finally {
      this.reconcilingSecurity--;
      if (stopped || this.closed) this.authority.lifecycle.pause();
    }
  }
  async removeDevice(id: string) { await this.authority.removeDevice(id); await this.loadDevices(false); }
  async deleteMachineRecords(id: string) { await this.authority.deleteMachineRecords(id); await this.refresh(); }

  private projectHeader(h: SecurityHeader) {
    return create(HeaderSchema, {
      version: 3,
      kind: h.kind,
      machineId: h.machine,
      sessionId: h.session,
      requestId: h.request,
      seq: h.seq,
      deadlineMs: h.deadline,
      sender: h.sender,
      compression: h.compression,
    });
  }
  private async historicalPlace(
    want: Parameters<typeof expect>[1],
    header: ReturnType<Remote["projectHeader"]>,
    history: boolean,
  ) {
    if (
      history &&
      [PayloadKind.EVENT, PayloadKind.IMAGE].includes(want.kind) &&
      header.machineId !== want.machineId
    ) {
      const view = this.sessions.get(want.sessionId ?? "");
      const origins = await machineReplacementOrigins(
        await this.security.verifiedRecord(),
        want.machineId ?? "",
        view?.info?.machineReplacements ?? [],
      );
      if (origins.has(header.machineId))
        return { ...want, machineId: header.machineId };
    }
    return want;
  }
  private async openSecureMessage<D extends DescMessage>(
    env: Envelope | undefined,
    want: Parameters<typeof expect>[1],
    schema: D,
    history = false,
    historical?: HistoryResult,
  ) {
    if (!env) throw new Error("缺少安全信封");
    if (
      [
        PayloadKind.ACCOUNT_PREFS,
        PayloadKind.DEVICE_META,
        PayloadKind.MACHINE_META,
        PayloadKind.SESSION_META,
        PayloadKind.IMAGE,
      ].includes(want.kind)
    )
      history = true;
    const seed = [
      PayloadKind.EVENT,
      PayloadKind.IMAGE,
      PayloadKind.TERMINAL_OUTPUT,
      PayloadKind.TERMINAL_INPUT,
    ].includes(want.kind)
      ? this.sessionSeeds.get(want.sessionId ?? "")
      : undefined;
    if (
      history &&
      [PayloadKind.EVENT, PayloadKind.IMAGE].includes(want.kind) &&
      fromBinary(HeaderSchema, env.header).version === 1
    ) {
      const rawSeed = this.sessionSeeds.get(want.sessionId ?? "");
      if (!rawSeed) throw new Error("旧历史的会话种子缺失");
      return openLegacyMessage(
        await importSessionKey(rawSeed),
        env,
        await this.historicalPlace(
          want,
          fromBinary(HeaderSchema, env.header),
          history,
        ),
        schema,
      );
    }
    let out: Awaited<ReturnType<DeviceSecurityClient["openHistory"]>>;
    try {
      if (historical?.status === "rejected") throw historical.reason;
      out = historical?.status === "fulfilled"
        ? historical.value
        : history
          ? await this.security.openHistory(env, seed)
          : await this.security.openCurrent(env, seed);
    } catch (e) {
      if (e instanceof RecoveryHistoryUnavailable)
        expect(
          this.projectHeader(e.header),
          await this.historicalPlace(
            want,
            this.projectHeader(e.header),
            history,
          ),
        );
      throw e;
    }
    const header = this.projectHeader(out.header);
    expect(header, await this.historicalPlace(want, header, history));
    let msg: MessageShape<D>;
    try {
      msg = fromBinary(schema, out.plaintext);
      validateSecurityBody(out.header, schema.typeName, msg);
    } catch {
      throw new Error("安全正文类型或方向不匹配");
    }
    return { header, msg, size: out.plaintext.length };
  }
  private async sealSecureMessage<D extends DescMessage>(
    h: Omit<
      Partial<import("./gen/remoteagent/v1/envelope_pb").Header>,
      "$typeName"
    >,
    schema: D,
    msg: MessageShape<D>,
    beforeSign?: () => void,
  ) {
    let purpose = Number(h.kind) as Purpose;
    if (h.kind === PayloadKind.TERMINAL_INPUT) {
      const f = msg as unknown as { kind: { case?: string } };
      if (f.kind.case === "resize") purpose = Purpose.TerminalResize;
      if (f.kind.case === "historyRequest") purpose = Purpose.TerminalHistory;
    }
    const seed = [1, 7, 8, 11, 14, 15].includes(purpose)
      ? this.sessionSeeds.get(h.sessionId ?? "")
      : undefined;
    return this.security.seal(
      {
        kind: Number(h.kind),
        purpose,
        machine: h.machineId ?? "",
        session: h.sessionId ?? "",
        request: h.requestId ?? "",
        seq: h.seq ?? 0n,
        deadline:
          h.deadlineMs ??
          (h.kind === PayloadKind.TERMINAL_INPUT
            ? BigInt(Date.now() + 60000)
            : 0n),
        compression: 0,
      },
      toBinary(schema, msg) as Uint8Array<ArrayBuffer>,
      seed,
      beforeSign,
    );
  }
  static async load(): Promise<Remote | undefined> {
    await resumeRegistration();
    const acct = await loadAccount();
    if (!acct) return undefined;
    const r = new Remote(
      acct,
      acct.root ? await deriveKeys(acct.root) : undefined,
      eventCache(),
    );
    // What the last visit kept is there for the first paint.
    await r.warm();
    return r;
  }

  // --- change notification for React ---

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  snapshot = () => this.version;

  /** Tells the views that state kept here changed. */
  touch() {
    this.changed();
  }

  get link(): Link {
    if (this.conn === "online") return "online";
    if (!this.network) return "no-network";
    return this.waited ? "no-server" : "waiting";
  }

  /** Cached content needs no banner for a brief reconnect. Cold starts do. */
  get showWaiting(): boolean {
    return this.waitingNotice || (!this.listed && this.sessions.size === 0);
  }

  /** Nothing here is known to still hold: the server is out of reach, or
   * has not been reached yet. A connection lost a moment ago does not count. */
  get unlinked(): boolean {
    const link = this.link;
    return (
      link === "no-network" ||
      link === "no-server" ||
      (link === "waiting" && !this.listed)
    );
  }

  /** Whether the device has a network at all. Losing it ends the
   * connection at once instead of at the next missed heartbeat. */
  setNetwork(available: boolean) {
    if (this.network === available) return;
    this.network = available;
    clearTimeout(this.graceTimer);
    this.graceTimer = undefined;
    this.waited = false;
    this.waitingNotice = false;
    this.changed(true);
    if (!available) this.reconnectNow(true);
  }

  protected changed(immediate = false) {
    if (this.conn === "online" || this.closed || !this.begun) {
      clearTimeout(this.graceTimer);
      this.graceTimer = undefined;
      this.waited = false;
      this.waitingNotice = false;
    } else if (!this.waited && this.graceTimer === undefined) {
      // Reuse the link grace timer: first reveal waiting, then declare the
      // server out of reach at the original total deadline.
      const notice = Math.min(RECONNECT_NOTICE_MS, this.linkGraceMs);
      this.graceTimer = setTimeout(() => {
        this.graceTimer = undefined;
        if (this.waitingNotice || notice === this.linkGraceMs) this.waited = true;
        this.waitingNotice = true;
        this.changed(true);
      }, this.waitingNotice ? this.linkGraceMs - notice : notice);
    }
    this.version++;
    // Connection state must reach React even while an older listing is
    // awaiting metadata. Batching that work must not hide recovery or loss.
    if (this.held && !immediate) this.dirty = true;
    else for (const fn of this.listeners) fn();
  }

  // While a listing is applied, each of its sessions is a change: the
  // views are told once, at the end.
  private held = 0;
  private dirty = false;

  private async quietly(fn: () => Promise<void>) {
    this.held++;
    try {
      await fn();
    } finally {
      if (!--this.held && this.dirty) {
        this.dirty = false;
        for (const fn of this.listeners) fn();
      }
    }
  }

  // What this browser kept from the last visit: the login token, and the
  // machines, sessions and project preferences as last read, and what each
  // machine said of its models and settings, shown until they answer again.
  private warming?: Promise<void>;

  private warm(): Promise<void> {
    return (this.warming ??= (async () => {
      // The former cache item is deleted, never imported or migrated.
      this.keepValue("token", undefined);
      try {
        const token = await loadLoginToken(this.account.server, this.account.deviceId);
        if (token && !this.closed && !this.token)
          [this.token, this.tokenExpires] = [token.token, token.expiresMs];
      } catch { /* Missing local state means a normal login. */ }
      if (!this.cache) return;
      const mine = (v: unknown): Record<string, unknown> | undefined => {
        const o = v as Record<string, unknown> | undefined;
        return o &&
          o.deviceId === this.account.deviceId &&
          o.server === this.account.server
          ? o
          : undefined;
      };
      try {
        const identity = await this.security.localIdentity();
        this.trustedMembers = new Map(
          identity.state.core.members.map((m) => [m.id, m]),
        );
        this.authorityDeviceId = identity.state.core.authorityID;
        this.archiveMachines = await this.security.historicalMachines();
        const [machines, sessions, prefs, info] = (
          await Promise.all(
            ["machines", "sessions", "prefs", "info"].map((k) =>
              this.cache!.get(k),
            ),
          )
        ).map(mine);
        if (machines && sessions && !this.listed)
          await this.quietly(async () => {
            for (const m of fromBinary(
              ListMachinesResponseSchema,
              machines.bytes as Uint8Array,
            ).machines) {
              await this.putMachine({ ...m, online: false });
            }
            for (const s of fromBinary(
              ListRemoteSessionsResponseSchema,
              sessions.bytes as Uint8Array,
            ).sessions) {
              await this.putSession(s);
            }
          });
        // Still "loading": the list is shown as it was, and changed only once the server's copy is read.
        if (prefs && !this.prefs.snap)
          this.prefs = {
            ...this.prefs,
            snap: await this.openPrefs(
              fromBinary(AccountPrefsObjectSchema, prefs.bytes as Uint8Array),
            ),
          };
        if (info) {
          for (const [k, b] of Object.entries(
            (info.models ?? {}) as Record<string, Uint8Array>,
          ))
            if (!this.modelLists.has(k))
              this.modelLists.set(k, fromBinary(ListModelsResponseSchema, b));
          for (const [k, b] of Object.entries(
            (info.settings ?? {}) as Record<string, Uint8Array>,
          ))
            if (!this.settingsRead.has(k))
              this.settingsRead.set(k, fromBinary(DaemonSettingsSchema, b));
        }
        if (prefs || info) this.changed();
      } catch {
        // Nothing kept, or not readable: the server has it all.
      }
    })());
  }

  private keepValue(name: string, value: Record<string, unknown> | undefined) {
    void this.cache
      ?.set(
        name,
        value && {
          ...value,
          deviceId: this.account.deviceId,
          server: this.account.server,
        },
      )
      .catch(() => undefined);
  }

  // --- connection ---

  start() {
    if (this.removed) return;
    if (
      !this.closed &&
      (this.ws || (this.generation > 0 && this.conn === "connecting"))
    ) {
      if (this.ws && this.conn === "online" &&
          Date.now() - this.lastFrameAt > STREAM_RESUME_IDLE_MS)
        this.reconnectNow(true);
      return;
    }
    this.closed = false;
    clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    this.authority.lifecycle.resume();
    this.unwatchSecurityPush?.();
    this.unwatchSecurityPush = watchSecurityPush(() => void this.syncSecurity());
    this.begun = true;
    this.trimHistory();
    clearInterval(this.securityTimer);
    // Foreground state pull: a lease renewal or removal reaches this page
    // within a minute, and an unknown state is fetched at once.
    this.securityTimer = setInterval(() => void this.syncSecurity(), 60_000);
    void this.connect();
  }

  private maintaining?: Promise<void>;
  /** Renews the lease and continues an unfinished management operation in
   * the background: neither the live stream nor the state pull waits for
   * the authority lock or its round trips. Only the admin, or a device with
   * an operation of its own (a handoff offer, a recovery), has anything to
   * do; the others skip it without taking the lock. */
  private maintainAuthority() {
    if (typeof navigator === "undefined" || !navigator.locks || this.maintaining) return;
    this.maintaining = (async () => {
      try {
        if (!this.isAuthority && !(await readSecurityOperation())) return;
        await this.authority.renewLeaseIfNeeded();
        this.authorityError = "";
        const record = await this.security.verifiedRecord();
        this.authorityDeviceId = record.states.at(-1)!.core.authorityID;
      } catch (e) {
        // An unresolved management operation does not stop history sync or
        // the connection. Control still checks the verified head lease.
        this.authorityError = errorText(e);
      } finally {
        this.maintaining = undefined;
        if (!this.closed) this.changed();
      }
    })();
  }

  async syncSecurity() {
    if (this.closed) return;
    try {
      const record = await this.security.sync();
      this.authorityDeviceId = record.states.at(-1)!.core.authorityID;
      this.maintainAuthority();
      this.changed();
      if (record.member === false) {
        this.removalVerified = true;
        this.discardToken();
        await this.tokenWrites;
        this.removed = true;
        this.stop();
        return;
      }
      this.clearClockWarning();
      const head = await this.security.localStateHash();
      if (
        this.recoveryObjectsPending ||
        !this.directoryHead ||
        !equalBytes(this.directoryHead, head)
      )
        // Only a successfully applied directory acknowledges this head.
        // A failed RPC must remain pending after security.sync advances it.
        await this.loadDevices(false);
      if (this.descriptionPending) await this.describeSelf();
    } catch (e) {
      this.report(e);
    }
  }

  private noteClock(e: unknown) {
    if (e instanceof SecurityError && e.code === "clock") {
      this.clockWarning =
        "本机时间比账户状态慢。请校准系统时间和时区，否则远程操作会被拒绝。";
      this.changed();
    }
  }

  private clearClockWarning() {
    if (this.clockWarning) {
      this.clockWarning = "";
      this.changed();
    }
  }

  stop() {
    this.closed = true;
    this.authority.lifecycle.pause();
    this.unwatchSecurityPush?.();
    this.unwatchSecurityPush = undefined;
    clearInterval(this.securityTimer);
    this.securityTimer = undefined;
    clearTimeout(this.historyTrim);
    this.historyTrim = undefined;
    this.generation++;
    clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    clearTimeout(this.openTimer);
    clearTimeout(this.idleTimer);
    const ws = this.ws;
    this.ws = undefined;
    ws?.close();
    for (const close of this.terminals.values()) close("终端连接已关闭");
    this.terminals.clear();
    if (this.conn === "online") this.lostAt = Date.now();
    this.conn = "offline";
    this.changed(true);
  }

  /** Connects now instead of waiting out the backoff, e.g. when the
   * network came back. */
  reconnectNow(networkChanged = false) {
    if (this.closed || this.removed) return;
    if (networkChanged) {
      if (this.conn === "online") this.lostAt = Date.now();
      this.generation++;
      clearTimeout(this.openTimer);
      clearTimeout(this.idleTimer);
      const ws = this.ws;
      this.ws = undefined;
      ws?.close();
      for (const close of this.terminals.values())
        close("网络已变化，正在重连");
    } else if (this.ws || this.retryTimer === undefined) return;
    clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    this.retry = 1000;
    void this.connect();
  }

  /** How long one round trip to the server takes now, in ms: a request
   * that does nothing there (连接检测). Rejects when no answer comes. */
  async pingServer(timeoutMs = 8000): Promise<number> {
    const stop = new AbortController();
    const timer = setTimeout(() => stop.abort(), timeoutMs);
    const started = performance.now();
    try {
      const res = await relayFetch(apiBase(this.account.server) + "/healthz", {
        cache: "no-store",
        signal: stop.signal,
      });
      await res.arrayBuffer();
      if (!res.ok) throw new Error(`服务器返回 ${res.status}`);
      return Math.round(performance.now() - started);
    } catch (e) {
      throw stop.signal.aborted ? new Error("没有回应") : e;
    } finally {
      clearTimeout(timer);
    }
  }

  /** How long one round trip to a computer takes, through the server and
   * back: a sealed command that only reads its settings. */
  async pingMachine(machineId: string, timeoutMs = 15_000): Promise<number> {
    const started = performance.now();
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      await Promise.race([
        this.machineCommand(machineId, { case: "getSettings", value: {} }),
        new Promise<never>(
          (_, reject) =>
            (timer = setTimeout(
              () => reject(new Error("没有回应")),
              timeoutMs,
            )),
        ),
      ]);
      return Math.round(performance.now() - started);
    } finally {
      clearTimeout(timer);
    }
  }

  /** One login at a time: the server keeps few challenges per device, and
   * a denial has to mean what it says. */
  private login(): Promise<void> {
    return (this.loggingIn ??= this.signIn().finally(
      () => (this.loggingIn = undefined),
    ));
  }

  private async signIn() {
    const auth = createClient(
      AuthService,
      createConnectTransport({
        fetch: relayFetch,
        baseUrl: apiBase(this.account.server),
        useBinaryFormat: true,
        defaultTimeoutMs: OPEN_TIMEOUT_MS,
      }),
    );
    for (let again = true; ; again = false) {
      try {
        const ch = await auth.getChallenge({ deviceId: this.account.deviceId });
        this.setupProgress?.();
        const sig = await signLogin(this.account.devicePrivate, ch.challenge);
        const res = await auth.login({
          protocolVersion: PROTOCOL_VERSION,
          deviceId: this.account.deviceId,
          challenge: ch.challenge,
          signature: sig,
        });
        this.setupProgress?.();
        if (this.closed) return;
        this.token = res.token;
        this.tokenExpires = Number(res.expiresMs);
        this.tokenWrites = this.tokenWrites.then(() => saveLoginToken(
          this.account.server, this.account.deviceId,
          { token: res.token, expiresMs: Number(res.expiresMs) },
        )).catch(() => undefined);
        await this.tokenWrites;
        return;
      } catch (e) {
        if (!(e instanceof ConnectError) || e.code !== Code.PermissionDenied)
          throw e;
        // A server restarted between the two calls forgets the challenge;
        // denied a fresh one too, it no longer knows this device.
        if (again) continue;
        this.discardToken();
        await this.tokenWrites;
        this.removed = true;
        this.stop();
        await this.checkLocalRemoval();
        throw e;
      }
    }
  }

  /** Tries the login again after the server turned this device away. */
  retryLogin() {
    this.removed = false;
    this.start();
  }

  private async connect() {
    if (this.closed) return;
    this.retryTimer = undefined;
    const generation = ++this.generation;
    this.conn = "connecting";
    this.changed(true);
    let step = "";
    let budget = OPEN_TIMEOUT_MS;
    let stall: ReturnType<typeof setTimeout> | undefined;
    const progress = () => {
      if (this.closed || generation !== this.generation) return;
      clearTimeout(stall);
      stall = setTimeout(() => {
        if (this.closed || generation !== this.generation) return;
        this.generation++;
        this.fail(new Error(`${step}超时，正在重连`));
      }, budget);
    };
    const at = (name: string, timeout = OPEN_TIMEOUT_MS) => {
      step = name;
      budget = timeout;
      progress();
    };
    this.setupProgress = progress;
    at("读取本机状态");
    try {
      const go = await this.preStream(generation, at);
      if (go) this.openStream(generation);
    } catch (e) {
      // Storage can reject before login, and WebSocket construction can
      // throw before callbacks exist. Both belong to this attempt's retry.
      if (!this.closed && generation === this.generation) this.fail(e);
    } finally {
      clearTimeout(stall);
      if (this.setupProgress === progress) this.setupProgress = undefined;
    }
  }

  /** Login and the security head, which the stream needs first. False
   * when this try ended here, failed or overtaken. */
  private async preStream(
    generation: number,
    at: (step: string, timeout?: number) => void,
  ): Promise<boolean> {
    await this.warm();
    if (this.closed || generation !== this.generation) return false;
    at("登录", SETUP_IDLE_MS);
    try {
      // A token kept from an earlier visit saves the two login calls. One
      // about to expire is not worth a refused connection; the server
      // refusing it closes the stream, and the next try logs in.
      if (!this.token || this.tokenExpires < Date.now() + 86_400_000) {
        this.discardToken();
        await this.tokenWrites;
        await this.login();
      }
    } catch (e) {
      if (generation === this.generation && !this.closed) this.fail(e);
      return false;
    }
    if (this.closed || generation !== this.generation) return false;
    at("同步设备状态", SETUP_IDLE_MS);
    try {
      // A stale head must not receive or open live frames; pull it before
      // the stream starts. The 60 s loop keeps it fresh afterwards.
      const record = await this.security.sync();
      this.authorityDeviceId = record.states.at(-1)!.core.authorityID;
      this.maintainAuthority();
      this.changed();
      if (record.member === false) {
        this.removalVerified = true;
        this.discardToken();
        await this.tokenWrites;
        this.removed = true;
        this.stop();
        return false;
      }
      this.clearClockWarning();
    } catch (e) {
      this.noteClock(e);
      if (generation === this.generation && !this.closed) this.fail(e);
      return false;
    }
    return !this.closed && generation === this.generation;
  }

  private lastFrameAt = 0;

  private openStream(generation: number) {
    const url =
      apiBase(this.account.server).replace(/^http/, "ws") + "/v1/stream";
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    const drop = (e: unknown, code?: number) => {
      if (this.ws !== ws) return;
      this.ws = undefined;
      this.generation++;
      clearTimeout(this.openTimer);
      clearTimeout(this.idleTimer);
      if (code === 1008) this.discardToken();
      ws.close();
      for (const close of this.terminals.values())
        close("与 server 的连接断开");
      this.fail(e);
    };
    const watch = () => {
      this.lastFrameAt = Date.now();
      clearTimeout(this.idleTimer);
      this.idleTimer = setTimeout(
        () => drop(new Error("连接超时，正在重连")),
        STREAM_IDLE_MS,
      );
    };
    // A browser gives up on a handshake whose packets are lost only after
    // minutes; the next attempt usually gets through much sooner.
    clearTimeout(this.openTimer);
    this.openTimer = setTimeout(() => {
      drop(new Error("连接超时，正在重连"));
    }, OPEN_TIMEOUT_MS);
    ws.onopen = () => {
      if (this.ws !== ws || this.closed || generation !== this.generation)
        return;
      const hello = create(PeerFrameSchema, {
        kind: { case: "hello", value: { token: this.token } },
      });
      ws.send(toBinary(PeerFrameSchema, hello));
    };
    let processing = Promise.resolve();
    let queuedBytes = 0;
    let queuedFrames = 0;
    let overflow = false;
    ws.onmessage = (m) => {
      if (this.closed || generation !== this.generation || overflow) return;
      watch();
      const data = m.data as ArrayBuffer;
      queuedBytes += data.byteLength;
      queuedFrames++;
      if (queuedBytes > 8 * 1024 * 1024 || queuedFrames > 1024) {
        overflow = true;
        drop(new Error("推送积压，正在重连"));
        return;
      }
      processing = processing
        .then(async () => {
          if (this.closed || generation !== this.generation || overflow) return;
          let f;
          try {
            f = fromBinary(ServerFrameSchema, new Uint8Array(data));
          } catch {
            overflow = true;
            drop(new Error("无效的推送帧"));
            return;
          }
          await this.onFrame(f.kind, generation);
        })
        .catch((e) => {
          this.report(e);
          drop(e);
        })
        .finally(() => {
          queuedBytes -= data.byteLength;
          queuedFrames--;
        });
    };
    ws.onclose = (e) => drop(new Error("与 server 的连接断开"), e?.code);
  }

  private fail(e: unknown) {
    if (this.conn === "online") this.lostAt = Date.now();
    this.conn = "offline";
    this.failures++;
    this.connError = errorText(e);
    this.error = errorText(e);
    this.changed(true);
    if (this.closed) return;
    const delay = this.retry;
    this.retry = Math.min(this.retry * 2, 30_000);
    clearTimeout(this.retryTimer);
    this.retryTimer = setTimeout(
      () => void this.connect(),
      delay + Math.random() * delay * 0.25,
    );
  }

  private async onFrame(
    kind: ReturnType<typeof fromBinary<typeof ServerFrameSchema>>["kind"],
    generation = this.generation,
  ) {
    if (this.closed || generation !== this.generation) return;
    switch (kind.case) {
      case "heartbeat":
        break;
      case "ready":
        clearTimeout(this.openTimer);
        this.retry = 1000;
        this.conn = "online";
        this.lostAt = undefined;
        this.everOnline = true;
        this.failures = 0;
        this.connError = "";
        this.error = "";
        this.changed(true);
        // Subscribed first, then catch up: nothing falls in between.
        await this.refresh();
        if (this.closed || generation !== this.generation) return;
        for (const sid of this.buffers.keys()) void this.fetchEvents(sid);
        void this.loadDevices();
        void this.loadPrefs();
        void restorePushPrefs(this).catch(() => undefined);
        break;
      case "accountPrefsUpdated":
        if (kind.value.object) await this.takePrefs(kind.value.object, true);
        break;
      case "deviceUpdated":
        if (kind.value.device) {
          this.devicePushed(kind.value.device.id);
          await this.putDevice(kind.value.device);
          this.changed();
        }
        break;
      case "deviceRemoved":
        void this.loadDevices(false);
        break;
      case "machineRemoved":
        void this.refresh();
        break;
      case "machineUpdated":
        if (kind.value.machine) {
          const id = kind.value.machine.id;
          this.bump(this.machinePushes, id);
          await this.loadDevices(false, { machineId: id, meta: kind.value.machine.meta });
          await this.putMachine(kind.value.machine);
        }
        break;
      case "sessionUpdated":
        if (kind.value.session) {
          this.bump(this.sessionPushes, kind.value.session.sessionId);
          await this.loadDevices(false, kind.value.session);
          await this.putSession(kind.value.session);
        }
        break;
      case "eventsAppended": {
        const sid = kind.value.sessionId;
        const s = this.sessions.get(sid);
        if (!s || !this.buffers.has(sid)) {
          if (s)
            s.lastSeq = maxBig(
              s.lastSeq,
              ...kind.value.events.map((e) => e.seq),
            );
          this.changed();
          break;
        }
        await this.ingest(s, kind.value.events, true, false, false);
        break;
      }
    }
  }

  /** Tells the views again once a retry is due: they ask as they render,
   * and nothing else may make them render by then. */
  private wake(ms: number) {
    setTimeout(() => !this.closed && this.changed(), ms + 50);
  }

  /** Whether a machine was removed since removals was copied from
   * removedPushes: an answer asked for before must not bring its
   * sessions back. */
  private goneSince(removals: Map<string, number>, machineId: string): boolean {
    return (
      (this.removedPushes.get(machineId) ?? 0) !==
      (removals.get(machineId) ?? 0)
    );
  }

  /** Whether a session held here lies past where the fetched pages end:
   * kept from before a reconnect, or fetched by its ID. Sessions not
   * fetched yet may come before it in the listing. */
  pastPages(s: SessionView): boolean {
    return (
      !!this.nextSessions &&
      s.endedMs !== 0n &&
      afterCursor(s, this.nextSessions)
    );
  }

  /** Drops a session of a machine that is gone, with its kept events:
   * no listing will say again that it no longer exists. */
  private forget(id: string) {
    this.sessions.delete(id);
    void this.cache?.drop(id).catch(() => undefined);
  }

  private bump(pushes: Map<string, number>, id: string) {
    pushes.set(id, (pushes.get(id) ?? 0) + 1);
  }

  /** Reloads machines and sessions. Pushes that arrive while the lists
   * are in flight are newer than them and win. */
  async refresh() {
    try {
      const mp = new Map(this.machinePushes);
      const sp = new Map(this.sessionPushes);
      const rp = new Map(this.removedPushes);
      const pushed = (
        now: Map<string, number>,
        then: Map<string, number>,
        id: string,
      ) => (now.get(id) ?? 0) !== (then.get(id) ?? 0);
      // From here on the cursor held belongs to the listing before: no
      // page is asked for with it, nor taken, until this one is in. It
      // still says which of the sessions held lie past the pages fetched.
      const listing = ++this.listing;
      this.relisting++;
      await this.loadDevices(false);
      if (this.devicesStatus !== "ready") throw new Error("无法核实当前成员");
      const [ms, ss] = await Promise.all([
        this.api.listMachines({}),
        this.api.listRemoteSessions({ limit: this.sessionPageSize }),
      ]);
      if (listing !== this.listing) return;
      // Forget what the server no longer has, e.g. a machine another
      // device removed while this one was offline. The first page holds
      // every session that has not ended and the latest ended ones: a
      // session held here and not on it is kept only if it belongs to a
      // later page, that is, it ended before the page's last.
      const machines = new Set(
        ms.machines
          .filter(
            (m) =>
              this.trustedMembers.get(m.id)?.kind === 1 ||
              this.archiveMachines.has(m.id),
          )
          .map((m) => m.id),
      );
      const sessions = new Set(ss.sessions.map((s) => s.sessionId));
      const later = (s: SessionView) =>
        !!ss.next && s.endedMs !== 0n && afterCursor(s, ss.next);
      // A machine found gone here counts as removed, as if its removal
      // had been pushed: a session of its that is being opened for a page
      // or a lookup asked for before must not be taken after this.
      const gone = new Set<string>();
      for (const id of this.machines.keys()) {
        if (machines.has(id) || pushed(this.machinePushes, mp, id)) continue;
        this.machines.delete(id);
        gone.add(id);
      }
      for (const [id, s] of this.sessions) {
        if (sessions.has(id) || pushed(this.sessionPushes, sp, id)) continue;
        if (!machines.has(s.machineId)) {
          this.forget(id);
          gone.add(s.machineId);
        } else if (!later(s)) this.sessions.delete(id);
      }
      for (const id of gone) this.bump(this.removedPushes, id);
      this.nextSessions = ss.next;
      this.moreSessions = !!ss.next;
      this.missing.clear();
      const removed = (id: string) => pushed(this.removedPushes, rp, id);
      await this.quietly(async () => {
        for (const m of ms.machines) {
          if (removed(m.id)) continue;
          await this.putMachine(m, pushed(this.machinePushes, mp, m.id));
        }
        for (const s of ss.sessions)
          if (!removed(s.machineId)) await this.putSession(s);
      });
      if (this.cache) {
        this.keepValue("machines", {
          bytes: toBinary(ListMachinesResponseSchema, ms),
        });
        this.keepValue("sessions", {
          bytes: toBinary(ListRemoteSessionsResponseSchema, ss),
        });
      }
      this.listed = true;
      this.listError = "";
      this.listRetry = 2000;
      void this.cache?.trim().catch(() => undefined);
      this.changed();
    } catch (e) {
      // The stream is up but the lists are not: say so, and try again
      // rather than wait for a reconnect that may never come.
      this.listError = errorText(e);
      this.report(e);
      clearTimeout(this.listTimer);
      const delay = this.listRetry;
      this.listRetry = Math.min(this.listRetry * 2, 30_000);
      this.listTimer = setTimeout(() => {
        if (!this.closed && this.conn === "online") void this.refresh();
      }, delay);
    } finally {
      // Pages may be asked for again; the views are told, since one that
      // asked meanwhile was turned away.
      this.relisting--;
      this.changed();
    }
  }

  /** Lists machines and sessions again now, after a failure. */
  /** Fetches the next page of ended sessions, if the server has one.
   * Calls while a page is on its way share it. Resolves to whether a
   * page arrived. */
  loadMoreSessions(): Promise<boolean> {
    if (this.paging) return this.paging;
    const after = this.nextSessions;
    if (
      !after ||
      this.relisting ||
      this.conn !== "online" ||
      Date.now() < this.pageRetryAt
    )
      return Promise.resolve(false);
    const listing = this.listing;
    this.loadingSessions = true;
    this.changed();
    this.paging = (async () => {
      try {
        const rp = new Map(this.removedPushes);
        const res = await this.api.listRemoteSessions({
          after,
          limit: this.sessionPageSize,
        });
        // The listing was started over meanwhile: this page continued the
        // one before, which may have held sessions that are gone since,
        // and it is not where the new listing continues.
        if (listing !== this.listing) return true;
        await this.quietly(async () => {
          for (const s of res.sessions)
            if (listing === this.listing && !this.goneSince(rp, s.machineId))
              await this.putSession(s);
        });
        // Opening the page took a while too: the cursor is only this
        // page's to move if the listing is still the one it continued.
        if (listing !== this.listing) return true;
        this.nextSessions = res.next;
        this.moreSessions = !!res.next;
        return true;
      } catch (e) {
        this.report(e);
        this.pageRetryAt = Date.now() + RETRY_MS;
        this.wake(RETRY_MS);
        return false;
      } finally {
        this.paging = undefined;
        this.loadingSessions = false;
        this.changed();
      }
    })();
    return this.paging;
  }

  /** Fetches every page that is left: filtering by title has to see all
   * the titles. Stops at the first page that fails. */
  async loadAllSessions() {
    if (this.loadingAll) return;
    this.loadingAll = true;
    try {
      while (this.moreSessions && (await this.loadMoreSessions()));
    } finally {
      this.loadingAll = false;
    }
  }

  /** Fetches a session that is not among the pages held: one opened from
   * a notification, a link or a fork's source. One that does not exist
   * is noted in missing. */
  async ensureSession(id: string) {
    if (
      !id ||
      this.sessions.has(id) ||
      this.missing.has(id) ||
      this.asking.has(id) ||
      this.conn !== "online"
    )
      return;
    // One that failed is not asked for again at once: callers ask on every render.
    if (Date.now() < (this.askRetryAt.get(id) ?? 0)) return;
    this.asking.add(id);
    try {
      const rp = new Map(this.removedPushes);
      const res = await this.api.listRemoteSessions({ sessionIds: [id] });
      const rs = res.sessions.find((s) => s.sessionId === id);
      // A session of a machine removed meanwhile, or one the listing of
      // machines no longer has, is as good as gone.
      if (
        rs &&
        !this.goneSince(rp, rs.machineId) &&
        (!this.listed || this.machines.has(rs.machineId))
      )
        await this.putSession(rs);
      // One whose key does not open is not held either, and asking again
      // would bring the same record: it cannot be shown.
      if (!this.sessions.has(id)) this.missing.add(id);
      this.askRetryAt.delete(id);
    } catch (e) {
      this.report(e);
      this.askRetryAt.set(id, Date.now() + RETRY_MS);
      this.wake(RETRY_MS);
    } finally {
      this.asking.delete(id);
      this.changed();
    }
  }

  retryList() {
    clearTimeout(this.listTimer);
    this.listError = "";
    this.changed();
    void this.refresh();
  }

  /** Stores a machine. staleOnline: a push arrived after m was fetched,
   * so its online flag is out of date. */
  private async putMachine(m: RemoteMachine, staleOnline = false) {
    if (!this.trustedMembers.has(m.id) && !this.archiveMachines.has(m.id))
      return;
    let view = this.machines.get(m.id);
    if (!view) {
      view = {
        id: m.id,
        online: m.online,
        lastSeenMs: m.lastSeenMs,
        metaRev: 0n,
      };
      this.machines.set(m.id, view);
    }
    if (!staleOnline) view.online = m.online;
    if (m.lastSeenMs > view.lastSeenMs) view.lastSeenMs = m.lastSeenMs;
    if (m.meta) {
      try {
        const { header, msg } = await this.openSecureMessage(
          m.meta,
          { kind: PayloadKind.MACHINE_META, machineId: m.id },
          MachineMetaSchema,
        );
        if (header.seq >= view.metaRev) {
          view.meta = msg;
          view.metaRev = header.seq;
        }
      } catch (e) {
        this.report(e);
      }
    }
    this.changed();
  }

  private async putSession(rs: RemoteSession) {
    if (
      !this.archiveMachines.has(rs.machineId) &&
      this.trustedMembers.get(rs.machineId)?.kind !== 1
    )
      return;
    let view = this.sessions.get(rs.sessionId);
    const removals = this.removedPushes.get(rs.machineId) ?? 0;
    try {
      if (!view) {
        if (!rs.key) throw new Error("缺少会话密钥信封");
        const route = fromBinary(HeaderSchema, rs.key.header);
        let header: import("./gen/remoteagent/v1/envelope_pb").Header,
          plaintext: Uint8Array;
        if (route.version === 1) {
          if (!this.legacyKeys)
            throw new Error("旧版历史需要账户根密钥，这台设备没有");
          const legacy = await openLegacyEnvelope(this.legacyKeys.wrap, rs.key);
          header = legacy.header;
          plaintext = legacy.plaintext;
        } else {
          const opened = await this.security.openHistory(rs.key);
          header = this.projectHeader(opened.header);
          plaintext = opened.plaintext;
        }
        if (plaintext.length !== 32) throw new Error("无效的会话种子");
        this.sessionSeeds.set(rs.sessionId, new Uint8Array(plaintext));
        expect(header, {
          kind: PayloadKind.SESSION_KEY,
          machineId: rs.machineId,
          sessionId: rs.sessionId,
        });
        const key = await importSessionKey(
          plaintext as Uint8Array<ArrayBuffer>,
        );
        // The machine may have been removed while the key was opened.
        if (
          (this.removedPushes.get(rs.machineId) ?? 0) !== removals ||
          (!this.archiveMachines.has(rs.machineId) &&
            this.trustedMembers.get(rs.machineId)?.kind !== 1)
        ) {
          this.sessionSeeds.delete(rs.sessionId);
          return;
        }
        view = this.sessions.get(rs.sessionId) ?? {
          id: rs.sessionId,
          machineId: rs.machineId,
          key,
          metaRev: 0n,
          lastSeq: 0n,
          updatedMs: 0n,
          endedMs: 0n,
        };
        this.sessions.set(rs.sessionId, view);
      }
      if (view.machineId !== rs.machineId) {
        const { msg } = await this.openSecureMessage(
          rs.meta,
          {
            kind: PayloadKind.SESSION_META,
            machineId: rs.machineId,
            sessionId: rs.sessionId,
          },
          SessionSchema,
        );
        const origins = await machineReplacementOrigins(
          await this.security.verifiedRecord(),
          rs.machineId,
          msg.machineReplacements,
        );
        if (!origins.has(view.machineId))
          throw new Error("会话记录的机器被改动");
        // Validate the new seed before changing an already displayed route.
        const opened = await this.security.openHistory(rs.key!);
        expect(this.projectHeader(opened.header), {
          kind: PayloadKind.SESSION_KEY,
          machineId: rs.machineId,
          sessionId: rs.sessionId,
        });
        if (opened.plaintext.length !== 32) throw new Error("无效的会话种子");
        this.sessionSeeds.set(rs.sessionId, new Uint8Array(opened.plaintext));
        view = {
          ...view,
          machineId: rs.machineId,
          metaRev: 0n,
          key: await importSessionKey(new Uint8Array(opened.plaintext)),
        };
        this.sessions.set(rs.sessionId, view);
      }
      view.lastSeq = maxBig(view.lastSeq, rs.lastSeq);
      view.updatedMs = maxBig(view.updatedMs, rs.updatedMs);
      if (rs.meta) {
        const { header, msg } = await this.openSecureMessage(
          rs.meta,
          {
            kind: PayloadKind.SESSION_META,
            machineId: rs.machineId,
            sessionId: rs.sessionId,
          },
          SessionSchema,
        );
        if (
          (this.removedPushes.get(rs.machineId) ?? 0) !== removals ||
          (!this.archiveMachines.has(rs.machineId) &&
            this.trustedMembers.get(rs.machineId)?.kind !== 1)
        )
          return;
        if (header.seq >= view.metaRev) {
          view.info = msg;
          if (msg.currentCheckpoint.length) {
            const cp = fromBinary(LiveSnapshotSchema, msg.currentCheckpoint);
            if (
              cp.session?.id !== rs.sessionId ||
              cp.session.lastSeq > msg.lastSeq ||
              cp.actions.some(
                (e) => e.sessionId !== rs.sessionId || e.seq > msg.lastSeq,
              )
            )
              throw new Error("密文无效：当前状态 checkpoint 不匹配");
            view.checkpoint = cp;
            view.checkpointStatus = "ready";
          } else view.checkpointStatus = "waiting";
          view.metaRev = header.seq;
          // The server stores when the session ended with the metadata,
          // so its place in the listing goes with the revision taken.
          view.endedMs = rs.endedMs;
        }
      }
    } catch (e) {
      this.report(e);
    }
    this.changed();
  }

  // --- events ---

  /** A continuous history segment; the latest page is shown while older pages load. */
  events(sid: string): Event[] {
    const b = this.buffers.get(sid);
    if (b) b.usedAt = Date.now();
    if (!b) {
      this.buffers.set(sid, {
        events: [],
        pending: new Map(),
        fetching: false,
        refetch: false,
        loaded: false,
        restored: false,
        error: "",
        broken: false,
        bytes: 0,
        usedAt: Date.now(),
      });
      void this.fetchEvents(sid);
      return [];
    }
    return b.recent ?? b.events;
  }

  loaded(sid: string): boolean {
    return this.buffers.get(sid)?.loaded ?? false;
  }

  history(sid: string): History {
    const b = this.buffers.get(sid);
    return {
      backfilling: !!b?.recent,
      loaded: b?.loaded ?? false,
      error: b?.error ?? "",
      broken: b?.broken ?? false,
      unavailable: b?.unavailableSeqs?.size ?? 0,
    };
  }

  /** Fetches a session's events again after a failure. */
  retryEvents(sid: string) {
    const b = this.buffers.get(sid);
    if (!b) return;
    b.error = "";
    b.broken = false;
    this.changed();
    void this.fetchEvents(sid);
  }

  /** Starts a history from the copy cached in this browser, so that only
   * what came since is downloaded. A copy that is longer than the server's
   * history or does not open belongs to something else: it is dropped. */
  private async restore(s: SessionView, b: EventBuffer) {
    b.restored = true;
    if (!this.cache || b.events.length) return;
    const stale = () => this.closed || this.buffers.get(s.id) !== b;
    let after = 0n;
    while (!stale()) {
      const rows = await this.cache.read(s.id, after).catch((): CachedEvent[] => []);
      if (!rows.length || stale()) return;
      if (rows[0].seq !== after + 1n || rows.at(-1)!.seq > s.lastSeq) break;
      await this.ingest(s, rows, false, true, true);
      if (stale()) return;
      if (b.broken) break;
      after = BigInt(b.events.length);
    }
    if (stale()) return;
    Object.assign(b, {
      events: [],
      pending: new Map(),
      broken: false,
      bytes: 0,
      inputStatuses: undefined,
      verifiedSeqs: undefined,
      verifiedEvents: undefined,
      verifiedDerived: undefined,
      unavailableSeqs: undefined,
      unverifiedLegacy: false,
    });
    await this.cache.drop(s.id).catch(() => undefined);
  }

  /** Fetches everything after the last contiguous event. */
  private async fetchEvents(sid: string) {
    const b = this.buffers.get(sid);
    const s = this.sessions.get(sid);
    if (!b || !s || this.closed) return;
    if (b.fetching) {
      b.refetch = true;
      return;
    }
    b.fetching = true;
    const generation = this.generation;
    let tailThrough: bigint | undefined;
    try {
      if (!b.restored && this.cache) await this.restore(s, b);
      // lastSeq is a routing hint, not authenticated event content. Every
      // returned envelope still passes the normal historical checks.
      if (!b.tailRequested && s.lastSeq > BigInt(b.events.length + this.historyPageSize)) {
        b.tailRequested = true;
        const tail = await this.api.getEvents({
          sessionId: sid,
          afterSeq: s.lastSeq - BigInt(this.historyPageSize),
          limit: this.historyPageSize,
        });
        if (this.closed || generation !== this.generation || this.buffers.get(sid) !== b)
          return;
        await this.ingest(s, tail.events, false, false, true);
        if (!tail.more && tail.events.length && !b.broken)
          tailThrough = tail.events.at(-1)!.seq;
      }
      do {
        b.refetch = false;
        for (;;) {
          if (
            this.closed ||
            generation !== this.generation ||
            this.buffers.get(sid) !== b
          )
            return;
          if (tailThrough !== undefined && BigInt(b.events.length) >= tailThrough &&
              s.lastSeq <= BigInt(b.events.length) && !b.pending.size) break;
          const before = b.events.length;
          const res = await this.api.getEvents({
            sessionId: sid,
            afterSeq: BigInt(before),
            limit: this.historyPageSize,
          });
          if (
            this.closed ||
            generation !== this.generation ||
            this.buffers.get(sid) !== b
          )
            return;
          await this.ingest(s, res.events, false, false, true);
          // An event that does not open stops progress; asking for the
          // same page again would not help.
          if (b.broken) break;
          if (b.events.length === before && b.pending.size)
            throw new Error("历史记录存在缺口，请重试");
          if (!res.more) {
            if (b.pending.size) throw new Error("历史记录存在缺口，请重试");
            break;
          }
          if (b.events.length === before)
            throw new Error("历史记录未能继续加载，请重试");
        }
      } while (b.refetch);
      b.loaded = true;
      b.error = "";
    } catch (e) {
      if (this.closed || generation !== this.generation) return;
      b.error = errorText(e);
      this.report(e);
    } finally {
      b.fetching = false;
      if (b.refetch && !this.closed && generation !== this.generation)
        void this.fetchEvents(sid);
      this.trimHistory();
      this.changed();
    }
  }

  private async ingest(
    s: SessionView,
    stored: StoredEvent[] | CachedEvent[],
    fillGaps = true,
    cached = false,
    history = true,
  ) {
    const b = this.buffers.get(s.id);
    if (!b) return;
    const fresh: CachedEvent[] = [];
    const save = () => {
      if (fresh.length && !cached)
        void this.cache?.append(s.id, fresh).catch(() => undefined);
    };
    const historical = new Map<StoredEvent | CachedEvent, HistoryResult>();
    if (history) {
      const rows = stored.filter(se => {
        if (se.seq <= BigInt(b.events.length) || b.pending.has(se.seq) || !se.envelope) return false;
        try {
          return fromBinary(HeaderSchema, se.envelope.header).version === 3;
        } catch {
          return false; // The normal per-row path reports malformed headers.
        }
      });
      if (rows.length) {
        try {
          const results = await this.security.openHistoryBatch(rows.map(se => ({
            envelope: se.envelope!, seed: this.sessionSeeds.get(s.id),
          })));
          rows.forEach((se, i) => historical.set(se, results[i]));
        } catch (reason) {
          rows.forEach(se => historical.set(se, { status: "rejected", reason }));
        }
      }
    }
    for (const se of stored) {
      const opened = historical.get(se);
      historical.delete(se);
      if (se.seq <= BigInt(b.events.length) || b.pending.has(se.seq)) continue;
      try {
        const { msg, size } = await this.openSecureMessage(
          se.envelope,
          {
            kind: PayloadKind.EVENT,
            machineId: s.machineId,
            sessionId: s.id,
            seq: se.seq,
          },
          EventSchema,
          history,
          opened,
        );
        if (this.buffers.get(s.id) !== b) return;
        if (msg.seq !== se.seq || msg.sessionId !== s.id)
          throw new Error("事件的位置被改动");
        if (se.seq <= BigInt(b.events.length) || b.pending.has(se.seq))
          continue;
        if (fromBinary(HeaderSchema, se.envelope!.header).version === 1)
          b.unverifiedLegacy = true;
        if (fromBinary(HeaderSchema, se.envelope!.header).version === 3)
          (b.verifiedSeqs ??= new Set()).add(se.seq);
        b.pending.set(se.seq, msg);
        // What is held is the opened event: a packed envelope is smaller than it.
        b.bytes = (b.bytes ?? 0) + size + 256;
        fresh.push({ seq: se.seq, envelope: se.envelope! });
      } catch (e) {
        if (e instanceof RecoveryHistoryUnavailable) {
          if (this.buffers.get(s.id) !== b) return;
          if (se.seq <= BigInt(b.events.length) || b.pending.has(se.seq))
            continue;
          // A local empty slot preserves the authenticated sequence cursor;
          // the unavailable body is never inserted into verifiedEvents.
          b.pending.set(
            se.seq,
            create(EventSchema, { seq: se.seq, sessionId: s.id }),
          );
          (b.unavailableSeqs ??= new Set()).add(se.seq);
          b.bytes = (b.bytes ?? 0) + 256;
          fresh.push({ seq: se.seq, envelope: se.envelope! });
          continue;
        }
        b.broken = true;
        this.report(e);
        break;
      }
    }
    save();
    if (this.buffers.get(s.id) !== b) return;
    const before = b.events.length;
    for (let next = BigInt(b.events.length + 1); b.pending.has(next); next++) {
      const event = b.pending.get(next)!;
      if (b.verifiedSeqs?.has(next)) (b.verifiedEvents ??= []).push(event);
      if (b.verifiedSeqs?.has(next) && event.kind.case === "inputState") {
        const { requestId, status } = event.kind.value;
        (b.inputStatuses ??= new Map()).set(requestId, status);
        // A message someone canceled is gone, and its local image copies
        // with it. One the session ended before writing can still be put
        // back into the input: it keeps them for that.
        if (status === InputStatus.CANCELED && !endedUnsent(event.kind.value)) {
          this.outboxes.set(
            s.id,
            this.outbox(s.id).filter((o) => o.sendId !== requestId),
          );
        }
      }
      b.events.push(event);
      b.pending.delete(next);
    }
    if (b.tailRequested && b.pending.size && !b.broken) {
      // Show only one contiguous suffix. Never run the timeline reducer over
      // a prefix concatenated with events beyond an unfilled gap.
      let last = 0n;
      for (const seq of b.pending.keys()) if (seq > last) last = seq;
      let first = last;
      while (b.pending.has(first - 1n)) first--;
      b.recent = [];
      for (let seq = first; seq <= last; seq++) b.recent.push(b.pending.get(seq)!);
    } else b.recent = undefined;
    s.lastSeq = maxBig(s.lastSeq, BigInt(b.events.length));
    this.recorded(
      s.id,
      recordedIds(
        b.events.slice(before).filter((e) => b.verifiedSeqs?.has(e.seq)),
      ),
    );
    if (fillGaps && b.pending.size > 0) void this.fetchEvents(s.id);
    this.changed();
  }

  // --- commands ---

  async refreshLiveSnapshot(sid: string): Promise<void> {
    if (this.liveJobs.has(sid)) return this.liveJobs.get(sid);
    const job = (async () => {
      try {
        const s = this.sessions.get(sid);
        if (!s) throw new Error("未知会话");
        const reply = await this.forward(s.machineId, sid, {
          case: "getLiveSnapshot",
          value: { sessionId: sid },
        });
        if (
          reply.kind.case !== "liveSnapshot" ||
          reply.kind.value.session?.id !== sid
        )
          throw new Error("当前请求快照无效");
        this.liveSnapshots.set(sid, reply.kind.value);
        this.liveErrors.delete(sid);
        if (s.info) s.info = { ...s.info, ...reply.kind.value.session };
        this.changed();
      } catch {
        this.liveSnapshots.delete(sid);
        this.liveErrors.set(
          sid,
          "无法核实电脑上的待处理请求，请重试或在电脑终端处理",
        );
        this.changed();
      } finally {
        this.liveJobs.delete(sid);
      }
    })();
    this.liveJobs.set(sid, job);
    return job;
  }
  historicalDerived(sid: string): Derived {
    const events = this.events(sid), agent = agentOf(this.sessions.get(sid));
    const cached = this.historicalDerivations.get(events);
    if (cached && cached.count === events.length && cached.agent === agent) return cached.value;
    const value = derive(events, agent);
    this.historicalDerivations.set(events, { count: events.length, agent, value });
    return value;
  }

  currentDerived(sid: string, d: Derived): Derived {
    const buffer = this.buffers.get(sid);
    if (buffer?.unverifiedLegacy) {
      const verified = buffer.verifiedEvents ?? [],
        agent = agentOf(this.sessions.get(sid));
      if (
        !buffer.verifiedDerived ||
        buffer.verifiedDerived.count !== verified.length ||
        buffer.verifiedDerived.agent !== agent
      )
        buffer.verifiedDerived = {
          count: verified.length,
          agent,
          value: derive(verified, agent),
        };
      d = buffer.verifiedDerived.value;
    }
    const live = this.liveSnapshots.get(sid);
    const checkpoint = this.sessions.get(sid)?.checkpoint;
    const agent = agentOf(this.sessions.get(sid));
    // The complete prefix may grow while the displayed recent suffix stays
    // unchanged; that can make the plan text named by a live snapshot readable.
    const events = buffer?.events, count = events?.length ?? 0;
    const cached = this.currentDerivations.get(d);
    if (cached && cached.sid === sid && cached.live === live &&
        cached.checkpoint === checkpoint && cached.agent === agent &&
        cached.events === events && cached.count === count) return cached.value;
    const out = {
      ...d,
      approvals: new Map(),
      questions: new Map(),
      plan: undefined,
    } as Derived;
    this.currentDerivations.set(d, { sid, live, checkpoint, agent, events, count, value: out });
    if (!live) {
      if (checkpoint) out.running = checkpoint.running;
      return out;
    }
    const observed = derive(live.actions, agentOf(this.sessions.get(sid)));
    out.running = live.running;
    out.inputs = new Map(d.inputs);
    out.paused = new Set(d.paused);
    out.stages = new Map(d.stages);
    out.inputReasons = new Map(d.inputReasons);
    out.accepted = new Map(d.accepted);
    for (const [id, status] of out.inputs)
      if (
        [InputStatus.QUEUED, InputStatus.HELD_BY_CLI].includes(status) &&
        !observed.inputs.has(id)
      ) {
        out.inputs.set(id, InputStatus.UNCONFIRMED);
        out.paused.delete(id);
        out.stages.delete(id);
      }
    for (const [id, status] of observed.inputs) {
      out.inputs.set(id, status);
      if (observed.paused.has(id)) out.paused.add(id);
      else out.paused.delete(id);
    }
    for (const [id, stage] of observed.stages) out.stages.set(id, stage);
    for (const [id, reason] of observed.inputReasons)
      out.inputReasons.set(id, reason);
    for (const [id, accepted] of observed.accepted)
      out.accepted.set(id, accepted);
    for (const event of live.actions) {
      if (event.kind.case === "approvalRequested")
        out.approvals.set(event.kind.value.approvalId, event.kind.value);
      if (event.kind.case === "questionAsked" && !event.kind.value.async)
        out.questions.set(event.kind.value.questionId, event.kind.value);
    }
    if (
      live.planEventSeq > 0n &&
      live.planScreenHash.length === 32 &&
      live.session?.planOpen
    ) {
      const b = this.buffers.get(sid);
      out.plan = create(PlanProposedSchema, {
        text: livePlanText(
          live,
          b?.events ?? [],
          (seq) =>
            !this.unverifiedEvent(sid, seq) && !b?.unavailableSeqs?.has(seq),
        ),
      });
    }
    return out;
  }
  liveSnapshotError(sid: string): string | undefined {
    return (
      this.liveErrors.get(sid) ??
      (this.sessions.get(sid)?.checkpointStatus === "waiting"
        ? "等待所属电脑更新当前状态"
        : undefined)
    );
  }
  unverifiedEvent(sid: string, seq: bigint): boolean {
    const b = this.buffers.get(sid);
    return !!b?.unverifiedLegacy && !b.verifiedSeqs?.has(seq);
  }
  hasUnverifiedHistory(sid: string): boolean {
    return this.buffers.get(sid)?.unverifiedLegacy ?? false;
  }
  /** Sends a command about a session. slot, when given, is the request
   * ID and deadline of an earlier attempt of the same command. */
  async command(
    sid: string,
    cmd: CommandKind,
    slot = newSlot(),
  ): Promise<CommandResult> {
    const authorized = this.authorizedCommands.get(slot);
    if (authorized)
      return this.forward(
        authorized.machineId,
        authorized.sid,
        authorized.cmd,
        slot,
      );
    const s = this.sessions.get(sid);
    if (!s) throw new Error("未知会话");
    if (
      cmd?.case === "resolveApproval" ||
      cmd?.case === "answerQuestion" ||
      cmd?.case === "answerPlan"
    ) {
      const action = cmd;
      const shown = this.liveSnapshots.get(sid);
      if (!shown) throw new Error("当前请求尚未由电脑核实，请等待同步");
      const key =
        action.case === "resolveApproval"
          ? action.value.approvalId
          : action.case === "answerQuestion"
            ? action.value.questionId
            : "";
      const displayed = shown.actions.find((e) =>
        action.case === "resolveApproval"
          ? e.kind.case === "approvalRequested" &&
            e.kind.value.approvalId === key
          : e.kind.case === "questionAsked" && e.kind.value.questionId === key,
      );
      const observed =
        action.case === "answerPlan"
          ? shown.planEventSeq
          : (displayed?.seq ?? 0n);
      if (observed === 0n) throw new Error("当前请求已不可操作");
      await this.refreshLiveSnapshot(sid);
      const fresh = this.liveSnapshots.get(sid);
      if (!fresh) throw new Error("无法重新核实当前请求");
      const current = fresh.actions.find((e) =>
        action.case === "resolveApproval"
          ? e.kind.case === "approvalRequested" &&
            e.kind.value.approvalId === key
          : e.kind.case === "questionAsked" && e.kind.value.questionId === key,
      );
      if (action.case === "answerPlan") {
        if (
          fresh.planEventSeq !== observed ||
          !equalBytes(fresh.planScreenHash, shown.planScreenHash)
        )
          throw new Error("计划画面已变化，请重新核对");
        cmd = {
          case: "answerPlan",
          value: {
            ...action.value,
            observedEventSeq: observed,
            observedScreenHash: shown.planScreenHash,
          },
        };
      } else {
        if (
          current?.seq !== observed ||
          !displayed ||
          !equalBytes(
            toBinary(EventSchema, current),
            toBinary(EventSchema, displayed),
          )
        )
          throw new Error("电脑上的请求已变化，请重新核对");
        if (action.case === "resolveApproval")
          cmd = {
            case: "resolveApproval",
            value: { ...action.value, observedEventSeq: observed },
          };
        else
          cmd = {
            case: "answerQuestion",
            value: { ...action.value, observedEventSeq: observed },
          };
      }
    }
    return this.forward(s.machineId, sid, cmd, slot);
  }

  /** Sends a command about a machine, such as starting a session. */
  machineCommand(
    machineId: string,
    cmd: CommandKind,
    slot?: RequestSlot,
  ): Promise<CommandResult> {
    return this.forward(machineId, "", cmd, slot);
  }

  // A request slot owns its first authorized plaintext, including its observation.
  // A retry may use a new content key but must reach the accepted log unchanged.
  private readonly authorizedCommands = new WeakMap<
    RequestSlot,
    { machineId: string; sid: string; cmd: CommandKind }
  >();

  /** Seals cmd, forwards it and returns the opened result. Network
   * failures are retried with the same request ID, which the daemon
   * runs at most once. */
  private async forward(
    machineId: string,
    sid: string,
    cmd: CommandKind,
    slot = newSlot(),
  ): Promise<CommandResult> {
    const authorized = this.authorizedCommands.get(slot);
    if (authorized) ({ machineId, sid, cmd } = authorized);
    else
      cmd = fromBinary(
        CommandSchema,
        toBinary(CommandSchema, create(CommandSchema, { kind: cmd })),
      ).kind;
    const { requestId, deadline } = slot;
    // A machine of another protocol version would refuse the command, in
    // words that do not say which side to update: say so here, unsent.
    const m = this.machines.get(machineId);
    const theirs = m?.meta?.protocolVersion;
    if (theirs !== undefined && theirs !== PROTOCOL_VERSION)
      throw new CommandError(
        "failed_precondition",
        theirs < PROTOCOL_VERSION
          ? `${machineName(m)} 上的 remote-agent 版本较旧，请在电脑上更新后再试`
          : "这个网页版本较旧，请刷新或更新后再试",
      );
    // Signed by this device, so the daemon knows who sent it whatever the
    // server says (the server also checks the sender is the caller).
    let env: Envelope;
    try {
      env = await this.sealSecureMessage(
        {
          kind: PayloadKind.COMMAND,
          machineId,
          sessionId: sid,
          requestId,
          deadlineMs: BigInt(deadline),
          sender: this.account.deviceId,
        },
        CommandSchema,
        create(CommandSchema, { kind: cmd, protocolVersion: PROTOCOL_VERSION }),
      );
    } catch (e) {
      this.noteClock(e);
      if (e instanceof SecurityError && e.code === "clock")
        throw new CommandError(
          "clock",
          "本机时间比账户状态慢，请校准时间和时区后再操作；操作尚未发出",
        );
      if (e instanceof SecurityError && e.code === "lease")
        throw new CommandError(
          "failed_precondition",
          "远程控制有效期已过，控制和新内容上传已暂停；请在当前管理端续期",
        );
      throw new CommandError(
        "security_unavailable",
        "无法用当前安全状态签名，操作尚未发出；请重试",
      );
    }
    this.authorizedCommands.set(slot, { machineId, sid, cmd });
    // Once a try may have reached the daemon, no later failure of this
    // request can say it did not run.
    let mayHaveRun = false;
    for (let attempt = 0; ; attempt++) {
      try {
        // Once C is handed to an untrusted relay, no outer status proves it
        // was not executed. Only an authenticated daemon result is final.
        mayHaveRun = true;
        const res = await this.api.forward({ machineId, command: env });
        let msg: CommandResult;
        try {
          msg = (
            await this.openSecureMessage(
              res.result,
              {
                kind: PayloadKind.COMMAND_RESULT,
                machineId,
                sessionId: sid,
                requestId,
              },
              CommandResultSchema,
            )
          ).msg;
        } catch {
          throw new CommandError(
            "unknown",
            "电脑的回复未通过验证，不确定指令是否执行",
          );
        }
        if (msg.errorCode)
          throw new CommandError(msg.errorCode, msg.errorMessage, true);
        return msg;
      } catch (e) {
        // The daemon's answer is final; only transport failures are retried.
        if (!(e instanceof ConnectError)) throw e;
        const ce = e;
        if (ce.code === Code.Unauthenticated && attempt < 3) {
          try {
            await this.login();
          } catch {
            throw new CommandError(
              "unknown",
              "之前请求的结果仍未知，当前无法重新登录",
            );
          }
          continue;
        }
        // DeadlineExceeded is also the server's word for a daemon that gave
        // no result: it may have run.
        const unknown = [
          Code.DeadlineExceeded,
          Code.Unknown,
          Code.Internal,
          Code.Canceled,
        ].includes(ce.code);
        if (unknown) mayHaveRun = true;
        if (unknown && attempt < 3 && Date.now() + 2000 < deadline) {
          await new Promise((r) => setTimeout(r, 1000 * (attempt + 1)));
          continue;
        }
        if (ce.code === Code.Unavailable)
          throw mayHaveRun
            ? new CommandError("unknown", "连接中途断了")
            : new CommandError("unavailable", "电脑不在线，指令没有发出");
        if (unknown) throw new CommandError("unknown", "连接超时");
        if (mayHaveRun)
          throw new CommandError(
            "unknown",
            "中继未提供可验证的执行结果，请重试同一请求",
          );
        throw e;
      }
    }
  }

  /** Sends a chat message. A retry passes the first attempt's slot, so
   * the daemon answers it from its log. Only if that slot expired before
   * reaching the computer is a new one used; the send ID still keeps the
   * daemon from queueing the text twice. Returns the slot that got an
   * answer. */
  async sendMessage(
    sid: string,
    text: string,
    sendId: string,
    slot: RequestSlot,
    wasUnknown = false,
    imageIds: string[] = [],
    mode = SendMode.STEER,
  ): Promise<RequestSlot> {
    const cmd: CommandKind = {
      case: "send",
      value: { sessionId: sid, text, requestId: sendId, imageIds, mode },
    };
    try {
      await this.command(sid, cmd, slot);
      return slot;
    } catch (e) {
      // A send whose outcome was unknown is never sealed afresh.
      if (
        wasUnknown ||
        !(e instanceof CommandError) ||
        e.code !== "deadline_exceeded" ||
        Date.now() < slot.deadline
      )
        throw e;
      const fresh = newSlot();
      await this.command(sid, cmd, fresh);
      return fresh;
    }
  }

  // --- the outbox: chat messages sent from this page ---

  protected outboxes = new Map<string, Outgoing[]>();

  /** Messages sent from this page to session sid, oldest first. */
  outbox(sid: string): Outgoing[] {
    return this.outboxes.get(sid) ?? [];
  }

  private patchOut(sid: string, sendId: string, patch: Partial<Outgoing>) {
    if (!this.outbox(sid).some((o) => o.sendId === sendId)) return;
    this.outboxes.set(
      sid,
      this.outbox(sid).map((o) =>
        o.sendId === sendId ? { ...o, ...patch } : o,
      ),
    );
    this.changed();
  }

  /** Sends text, and images uploaded to the machine, to a session; its
   * progress shows in outbox(sid). */
  send(
    sid: string,
    text: string,
    imageIds: string[] = [],
    thumbs: Blob[] = [],
    mode = SendMode.STEER,
    attachments?: Outgoing["attachments"],
  ): string {
    const o: Outgoing = {
      sendId: newRequestID(),
      ...newSlot(),
      text,
      imageIds,
      thumbs,
      mode,
      attachments,
      status: "sending",
    };
    this.outboxes.set(sid, [...this.outbox(sid), o]);
    this.changed();
    void this.deliver(sid, o);
    return o.sendId;
  }

  // Messages the daemon failed to write, by request ID, and the new
  // message sent in their place: each is sent again once at most.
  replaced = new Map<string, string>();

  /** Sends a message that failed before Enter again, as a new message,
   * once: pressing retry again, or after the new one went, does nothing. */
  resend(
    sid: string,
    failedId: string,
    text: string,
    imageIds: string[] = [],
    thumbs: Blob[] = [],
  ) {
    if (this.replaced.has(failedId)) return;
    this.replaced.set(failedId, this.send(sid, text, imageIds, thumbs));
  }

  /** The CLI recorded these messages: the history now holds them, so the
   * outbox lets go of their text, requests and local image copies. */
  recorded(sid: string, requestIds: string[]) {
    if (!requestIds.length) return;
    const ids = new Set(requestIds);
    const box = this.outbox(sid);
    if (!box.some((o) => ids.has(o.sendId))) return;
    const left = box.filter((o) => !ids.has(o.sendId));
    if (left.length) this.outboxes.set(sid, left);
    else this.outboxes.delete(sid);
    this.changed();
  }

  /** Sends a message that could not be confirmed again, with the same
   * request ID and deadline. */
  retrySend(sid: string, sendId: string) {
    const o = this.outbox(sid).find((x) => x.sendId === sendId);
    if (o && o.status === "failed") void this.deliver(sid, o);
  }

  /** Forgets a failed message. */
  dismissSend(sid: string, sendId: string) {
    this.outboxes.set(
      sid,
      this.outbox(sid).filter((o) => o.sendId !== sendId),
    );
    this.changed();
  }

  private async deliver(sid: string, o: Outgoing) {
    this.patchOut(sid, o.sendId, {
      status: "sending",
      error: undefined,
      missingImages: false,
    });
    try {
      const slot = await this.sendMessage(
        sid,
        o.text,
        o.sendId,
        { requestId: o.requestId, deadline: o.deadline },
        o.unknown,
        o.imageIds,
        o.mode,
      );
      this.patchOut(sid, o.sendId, { status: "sent", ...slot });
    } catch (e) {
      // Once unknown, it stays unknown and keeps its request (attempt.ts);
      // the send ID also keeps the daemon from queueing the text twice.
      const still = stillUnknown(e, o.unknown ?? false);
      const missingImages =
        !still &&
        e instanceof CommandError &&
        e.code === "not_found" &&
        !!o.imageIds?.length &&
        e.message.includes("is not on this machine; upload it again");
      this.patchOut(sid, o.sendId, {
        status: "failed",
        error: missingImages
          ? "图片不在电脑上"
          : errorText(e),
        missingImages,
        unknown: still,
        ...(still ? {} : newSlot()),
      });
    }
  }

  /** Joins a session's terminal stream. Frames are sealed with the session
   * key and numbered; each side refuses frames out of order. */
  async openTerminal(
    sid: string,
    onFrame: (f: TerminalFrame) => void | Promise<void>,
    onClose: (reason: string) => void,
    signal?: AbortSignal,
  ): Promise<TermConn> {
    const s = this.sessions.get(sid);
    if (!s) throw new Error("未知会话");
    if (signal?.aborted) throw new Error("终端连接已取消");
    const streamId = newRequestID();
    const ws = new WebSocket(
      apiBase(this.account.server).replace(/^http/, "ws") + "/v1/term",
    );
    ws.binaryType = "arraybuffer";
    let closed = false;
    let openingTimer: ReturnType<typeof setTimeout> | undefined;
    let rejectOpening: (e: Error) => void = () => {};
    const abort = () => finish("终端连接已取消");
    const finish = (reason: string, notify = true) => {
      if (closed) return;
      closed = true;
      clearTimeout(openingTimer);
      signal?.removeEventListener("abort", abort);
      this.terminals.delete(ws);
      ws.close();
      rejectOpening(new Error(reason));
      if (notify) onClose(reason);
    };
    this.terminals.set(ws, finish);
    signal?.addEventListener("abort", abort, { once: true });
    await new Promise<void>((resolve, reject) => {
      rejectOpening = reject;
      openingTimer = setTimeout(
        () => finish("连接终端超时，请重新连接"),
        OPEN_TIMEOUT_MS,
      );
      ws.onopen = () => {
        if (closed) return;
        clearTimeout(openingTimer);
        try {
          ws.send(
            toBinary(
              TermHelloSchema,
              create(TermHelloSchema, {
                token: this.token,
                streamId,
                machineId: s.machineId,
              }),
            ),
          );
          resolve();
        } catch (e) {
          finish(errorText(e));
        }
      };
      ws.onerror = () => finish("连不上终端");
      ws.onclose = () => finish("终端连接已关闭");
    });
    let last = 0n;
    let queue = Promise.resolve();
    let pendingBytes = 0;
    let pendingFrames = 0;
    ws.onmessage = (m) => {
      if (closed) return;
      const size = (m.data as ArrayBuffer).byteLength;
      pendingBytes += size;
      pendingFrames++;
      if (pendingBytes > 2 * 1024 * 1024 || pendingFrames > 512) {
        finish("终端输出积压，请重新连接以获取最新画面");
        ws.close();
        return;
      }
      // Open in arrival order.
      queue = queue.then(async () => {
        try {
          if (closed) return;
          const env = fromBinary(
            EnvelopeSchema,
            new Uint8Array(m.data as ArrayBuffer),
          );
          const { header, msg } = await this.openSecureMessage(
            env,
            {
              kind: PayloadKind.TERMINAL_OUTPUT,
              machineId: s.machineId,
              sessionId: sid,
              requestId: streamId,
            },
            TerminalFrameSchema,
          );
          if (closed) return;
          if (header.seq <= last) throw new Error("终端帧顺序不对");
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
    ws.onclose = () => finish("终端连接断开");
    if (closed) throw new Error("终端连接已关闭");
    try {
      await this.command(sid, {
        case: "openTerminal",
        value: { sessionId: sid, streamId },
      });
    } catch (e) {
      finish(errorText(e));
      throw e;
    }
    if (closed) throw new Error("终端连接已关闭");
    let seq = 0n;
    let sending = Promise.resolve();
    return {
      send: (frame) => {
        sending = sending.then(async () => {
          if (closed || ws.readyState !== WebSocket.OPEN) return;
          seq++;
          const env = await this.sealSecureMessage(
            {
              kind: PayloadKind.TERMINAL_INPUT,
              machineId: s.machineId,
              sessionId: sid,
              requestId: streamId,
              seq,
              sender: this.account.deviceId,
            },
            TerminalFrameSchema,
            create(TerminalFrameSchema, frame),
          );
          if (!closed && ws.readyState === WebSocket.OPEN)
            ws.send(toBinary(EnvelopeSchema, env));
        });
      },
      close: () => finish("终端连接已关闭", false),
    };
  }

  private async republishRecoveryObjects(
    record: import("./device-security-store").SecurityRecord,
  ) {
    const boundary = record.states.filter((_, i) => record.proofs[i]).at(-1)
      ?.core.contentEpoch;
    if (!boundary) return;
    for (let attempt = 0; attempt < 5; attempt++) {
      const { object: o } = await this.api.getAccountPrefs({
        objectId: PREFS_OBJECT,
      });
      if (
        !o?.envelope ||
        o.revision === 0n ||
        parseHeader(new Uint8Array(o.envelope.securityHeader)).contentEpoch >=
          boundary
      )
        break;
      let snapshot;
      try {
        snapshot = await this.security.openMessage(
          o.envelope,
          ProjectPrefsSchema,
          true,
        );
      } catch (e) {
        if (e instanceof SecurityError && e.code === "unavailable") break;
        throw e;
      }
      if (
        snapshot.header.purpose !== Purpose.Prefs ||
        snapshot.header.seq !== o.revision ||
        snapshot.header.request !== PREFS_OBJECT
      )
        throw new SecurityError("untrusted");
      const envelope = await this.security.seal(
        {
          kind: 12,
          purpose: Purpose.Prefs,
          machine: "",
          session: "",
          request: PREFS_OBJECT,
          seq: o.revision + 1n,
          deadline: 0n,
          compression: 0,
        },
        toBinary(ProjectPrefsSchema, snapshot.msg),
      );
      if (
        (
          await this.api.putAccountPrefs({
            objectId: PREFS_OBJECT,
            expectedRevision: o.revision,
            envelope,
          })
        ).applied
      )
        break;
      if (attempt === 4) throw new Error("恢复对象同时更新，将在下次同步重试");
    }
    for (let attempt = 0; attempt < 5; attempt++) {
      const own = (await this.api.listDevices({})).devices.find(
        (d) => d.id === this.account.deviceId,
      );
      if (
        !own?.meta ||
        parseHeader(new Uint8Array(own.meta.securityHeader)).contentEpoch >=
          boundary
      )
        break;
      const snapshot = await this.security.openMessage(
        own.meta,
        DeviceMetaSchema,
        true,
      );
      if (
        snapshot.header.purpose !== Purpose.DeviceMeta ||
        snapshot.header.sender !== own.id ||
        snapshot.header.seq !== own.metaRevision
      )
        throw new SecurityError("untrusted");
      const meta = await this.security.seal(
        {
          kind: 13,
          purpose: Purpose.DeviceMeta,
          machine: "",
          session: "",
          request: own.id,
          seq: own.metaRevision + 1n,
          deadline: 0n,
          compression: 0,
        },
        toBinary(DeviceMetaSchema, snapshot.msg),
      );
      if (
        (
          await this.api.putDeviceMeta({
            deviceId: own.id,
            expectedRevision: own.metaRevision,
            meta,
          })
        ).applied
      )
        break;
      if (attempt === 4) throw new Error("恢复对象同时更新，将在下次同步重试");
    }
  }

  // --- devices ---

  /** Lists the account's devices from the signed membership state and the
   * server's sealed metadata objects. */
  async loadDevices(describe = true, update?: { machineId: string; meta?: Envelope }): Promise<void> {
    try {
      // Updates still pull and verify the security chain before trusting a
      // cached directory. Payload signatures and routes are checked on open.
      const record = await this.security.sync();
      const reconciliationPending = update ? !!(await this.authority.pending()) : false;
      const state = record.stateHash;
      if (!equalBytes(state, await this.security.localStateHash()))
        return this.loadDevices(describe, update);
      if (this.directoryJob) {
        const job = this.directoryJob;
        try { await job.promise; }
        finally { if (this.directoryJob === job) this.directoryJob = undefined; }
        if (job.invalidated || !equalBytes(job.head, await this.security.localStateHash()))
          return this.loadDevices(describe, update);
        if (describe) await this.describeSelf();
        return;
      }
      let knownUpdate = true;
      if (update?.meta) {
        try {
          const h = parseHeader(new Uint8Array(update.meta.securityHeader));
          knownUpdate = h.sender === update.machineId &&
            record.states.some((s) => s.core.epoch === h.epoch);
        } catch { knownUpdate = false; }
      }
      if (
        update && knownUpdate && record.member &&
        this.devicesStatus === "ready" && this.directoryHead &&
        equalBytes(this.directoryHead, state) &&
        this.trustedMembers.get(update.machineId)?.kind === 1 &&
        record.states.at(-1)!.core.members.some((m) => m.id === update.machineId && m.kind === 1) &&
        !this.recoveryObjectsPending && !this.security.recoveryPending &&
        !reconciliationPending && !this.reconcilingSecurity && !this.maintaining
      ) {
        this.clearClockWarning();
        return;
      }
      const job = { head: state, promise: this.loadDirectory(record), invalidated: false };
      this.directoryJob = job;
      try { await job.promise; }
      finally { if (this.directoryJob === job) this.directoryJob = undefined; }
      if (job.invalidated) return this.loadDevices(describe, update);
      // Self-description can reload the directory while publishing metadata.
      // It must run after the shared job has finished and been cleared.
      if (describe && this.devicesStatus === "ready") await this.describeSelf();
    } catch (e) {
      this.noteClock(e);
      this.devicesStatus = "error";
      this.changed();
    }
  }

  private async loadDirectory(record: SecurityRecord) {
    this.recoveryObjectsPending = true;
    let publicationPending = false;
    try {
      try {
        await this.republishRecoveryObjects(record);
      } catch (e) {
        // Expired publication stays pending; it must not block stored reads.
        if (!(e instanceof SecurityError) || e.code !== "lease") throw e;
        publicationPending = true;
      }
      this.clearClockWarning();
      const head = record.states[record.states.length - 1];
      const members = new Map(head.core.members.map((m) => [m.id, m]));
      const state = await stateHash(head);
      const [listed, online] = await Promise.all([
        this.api.listDevices({}),
        this.api.listMachines({}),
      ]);
      const metadata = new Map<string, Envelope>();
      for (const d of listed.devices)
        if (d.meta?.securityHeader.length)
          metadata.set("device:" + d.id, d.meta);
      for (const m of online.machines)
        if (m.meta?.securityHeader.length)
          metadata.set("machine:" + m.id, m.meta);
      const nextMachines = new Map(this.machines);
      const nextDevices = new Map<string, DeviceInfo>();
      for (const m of head.core.members) {
        const hint = listed.devices.find((d) => d.id === m.id);
        const d: DeviceInfo = {
          id: m.id,
          kind: m.kind,
          createdMs: hint?.createdMs ?? 0n,
          lastSeenMs: hint?.lastSeenMs ?? 0n,
          metaRev: 0n,
        };
        const meta = metadata.get(
          (m.kind === 1 ? "machine:" : "device:") + m.id,
        );
        if (meta) {
          try {
            if (m.kind === 2) {
              const opened = await this.openSecureMessage(
                meta,
                { kind: PayloadKind.DEVICE_META, requestId: m.id },
                DeviceMetaSchema,
              );
              d.meta = opened.msg;
              d.metaRev = opened.header.seq;
            } else {
              const opened = await this.openSecureMessage(
                meta,
                { kind: PayloadKind.MACHINE_META, machineId: m.id },
                MachineMetaSchema,
              );
              const old = this.machines.get(m.id),
                status = online.machines.find((x) => x.id === m.id);
              nextMachines.set(m.id, {
                ...(old ?? {
                  id: m.id,
                  online: false,
                  lastSeenMs: 0n,
                  metaRev: 0n,
                }),
                online: status?.online ?? false,
                lastSeenMs: status?.lastSeenMs ?? old?.lastSeenMs ?? 0n,
                meta: opened.msg,
                metaRev: opened.header.seq,
              });
            }
          } catch (e) {
            if (!(e instanceof SecurityError) || e.code !== "unavailable")
              throw e;
            const known = this.devices.get(m.id);
            if (known) {
              d.meta = known.meta;
              d.metaRev = known.metaRev;
            }
            if (m.kind === 1) {
              const old = this.machines.get(m.id),
                status = online.machines.find((x) => x.id === m.id);
              nextMachines.set(m.id, {
                ...(old ?? {
                  id: m.id,
                  metaRev: 0n,
                  online: false,
                  lastSeenMs: 0n,
                }),
                online: status?.online ?? false,
                lastSeenMs: status?.lastSeenMs ?? old?.lastSeenMs ?? 0n,
              });
            }
          }
        } else if (m.kind === 1) {
          const old = this.machines.get(m.id),
            status = online.machines.find((x) => x.id === m.id);
          nextMachines.set(m.id, {
            ...(old ?? {
              id: m.id,
              online: false,
              lastSeenMs: 0n,
              metaRev: 0n,
            }),
            online: status?.online ?? false,
            lastSeenMs: status?.lastSeenMs ?? old?.lastSeenMs ?? 0n,
          });
        }
        nextDevices.set(m.id, d);
      }
      if (this.directoryJob?.invalidated) return;
      if (!equalBytes(await this.security.localStateHash(), state))
        throw new Error("设备目录状态已变化");
      for (const [id, d] of nextDevices) {
        if ((this.devices.get(id)?.metaRev ?? 0n) > d.metaRev)
          throw new Error("设备目录响应已过时");
      }
      this.archiveMachines = new Set(
        record.states.flatMap((s) =>
          s.core.members.filter((m) => m.kind === 1).map((m) => m.id),
        ),
      );
      for (const [id, m] of this.machines)
        if (this.archiveMachines.has(id) && !members.has(id))
          nextMachines.set(id, { ...m, online: false });
      this.machines = nextMachines;
      this.trustedMembers = members;
      this.devices = nextDevices;
      this.authorityDeviceId = head.core.authorityID;
      for (const id of this.machines.keys())
        if (!members.has(id) && !this.archiveMachines.has(id)) {
          this.machines.delete(id);
          for (const [sid, s] of this.sessions)
            if (s.machineId === id) this.forget(sid);
        }
      this.recoveryObjectsPending = publicationPending;
      this.directoryHead = state;
      this.devicesStatus = "ready";
      this.changed();
    } catch (e) {
      this.noteClock(e);
      this.devicesStatus = "error";
      this.changed();
    }
  }

  // Removals seen per device, so that metadata still opening when its
  // device goes does not bring it back.

  // Pushes seen per device, registered, changed or removed, so that a list
  // fetched before a push cannot undo it.
  private devicePushes = new Map<string, number>();

  private devicePushed(id: string) {
    if (this.directoryJob) this.directoryJob.invalidated = true;
    this.devicePushes.set(id, (this.devicePushes.get(id) ?? 0) + 1);
  }

  private async putDevice(_d: Device) {
    await this.loadDevices(false);
  }

  /** Says what this browser is, once, so other clients can name it. */
  private async describeSelf() {
    const me = this.devices.get(this.account.deviceId);
    if (!me || me.meta) {
      this.descriptionPending = false;
      return;
    }
    this.descriptionPending = true;
    try {
      await this.changeDeviceMeta(this.account.deviceId, (m) => ({
        name: m?.name ?? "",
        platform: "web",
        description: m?.description || describeBrowser(navigator.userAgent),
        handoffRecoveryEd: m?.handoffRecoveryEd,
      }));
      this.descriptionPending = false;
    } catch (e) {
      this.report(e);
    }
  }

  /** Renames this client; the name is sealed, the server sees none.
   * DeviceMeta is self-only in protocol 10. */
  renameDevice(id: string, name: string): Promise<void> {
    if (id !== this.account.deviceId)
      throw new Error("名称只能在那台设备上修改");
    return this.changeDeviceMeta(id, (m) => ({
      name: name.trim(),
      platform: m?.platform ?? "",
      description: m?.description ?? "",
      handoffRecoveryEd: m?.handoffRecoveryEd,
    }));
  }

  /** The offer is signed once under the authority lock. A lost CAS ack is
   * reconciled against verified self metadata; retry never changes its bytes. */
  private async publishHandoffOffer(
    publicKey: Uint8Array<ArrayBuffer>, check: () => void,
    persist: (request: Uint8Array<ArrayBuffer>) => Promise<void>, signed?: Uint8Array<ArrayBuffer>,
  ) {
    let request;
    if (signed) request = fromBinary(PutDeviceMetaRequestSchema, signed);
    else {
      await this.loadDevices(false);
      check();
      if (this.devicesStatus !== "ready") throw new Error("无法核实设备信息");
      const own = this.devices.get(this.account.deviceId), revision = own?.metaRev ?? 0n;
      const meta = await this.sealSecureMessage({ kind: PayloadKind.DEVICE_META,
        requestId: this.account.deviceId, seq: revision + 1n, sender: this.account.deviceId },
        DeviceMetaSchema, create(DeviceMetaSchema, { ...create(DeviceMetaSchema, own?.meta), handoffRecoveryEd: publicKey }), check);
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
      if (!own?.meta || own.metaRev <= request.expectedRevision || !equalBytes(own.meta.handoffRecoveryEd, publicKey))
        return false;
    }
    return true;
  }

  /** Replaces this device's metadata with change(current). If another tab
   * wrote first, the change is made again on what it wrote. */
  private async changeDeviceMeta(
    id: string,
    change: (
      m: DeviceMeta | undefined,
    ) => Omit<Partial<DeviceMeta>, "$typeName">,
    check: () => void = () => undefined,
  ) {
    if (id !== this.account.deviceId)
      throw new Error("名称只能在那台设备上修改");
    for (let i = 0; i < 5; i++) {
      await this.loadDevices(false);
      if (this.devicesStatus !== "ready") throw new Error("无法核实设备信息");
      check();
      const cur = this.devices.get(id),
        rev = cur?.metaRev ?? 0n;
      const env = await this.sealSecureMessage(
        {
          kind: PayloadKind.DEVICE_META,
          requestId: id,
          seq: rev + 1n,
          sender: this.account.deviceId,
        },
        DeviceMetaSchema,
        create(DeviceMetaSchema, change(cur?.meta)),
        check,
      );
      check();
      const r = await this.api.putDeviceMeta({
        deviceId: id,
        expectedRevision: rev,
        meta: env,
      });
      check();
      if (r.applied) {
        await this.loadDevices(false);
        return;
      }
    }
    throw new Error("设备信息正在变化，请重试");
  }

  // --- project list preferences ---

  protected prefsStore: PrefsStore = {
    get: async () => this.readTrustedPrefs(),
    put: async (expected, next) => {
      const env = await this.sealSecureMessage(
        {
          kind: PayloadKind.ACCOUNT_PREFS,
          requestId: PREFS_OBJECT,
          seq: expected + 1n,
          sender: this.account.deviceId,
        },
        ProjectPrefsSchema,
        next,
      );
      const r = await this.api.putAccountPrefs({
        objectId: PREFS_OBJECT,
        expectedRevision: expected,
        envelope: env,
      });
      return {
        applied: r.applied,
        current: await this.openPrefs(r.object),
      };
    },
  };
  private async readTrustedPrefs(): Promise<PrefsSnapshot> {
    const r = await this.api.getAccountPrefs({ objectId: PREFS_OBJECT });
    return this.openPrefs(r.object);
  }
  private async openPrefs(
    o: AccountPrefsObject | undefined,
  ): Promise<PrefsSnapshot> {
    if (!o || !o.envelope || o.revision === 0n)
      return { revision: 0n, prefs: emptyPrefs() };
    const { msg, header } = await this.openSecureMessage(
      o.envelope,
      {
        kind: PayloadKind.ACCOUNT_PREFS,
        requestId: PREFS_OBJECT,
        seq: o.revision,
      },
      ProjectPrefsSchema,
    );
    if (msg.schema !== 1 || header.seq !== o.revision)
      throw new Error("项目偏好格式不受支持");
    return { revision: o.revision, prefs: msg };
  }

  /** Keeps the server's copy, sealed as it came, for the next visit's first paint. */
  private keepPrefs(o: AccountPrefsObject | undefined) {
    if (o && this.cache)
      this.keepValue("prefs", { bytes: toBinary(AccountPrefsObjectSchema, o) });
  }

  /** The account's pinned and removed projects; empty until read. */
  projectPrefs(): ProjectPrefs {
    return this.prefs.snap?.prefs ?? emptyPrefs();
  }

  async loadPrefs() {
    try {
      const r = await this.api.getAccountPrefs({ objectId: PREFS_OBJECT });
      await this.takePrefs(r.object, false);
      this.prefs = { ...this.prefs, status: "ready", error: "" };
    } catch (e) {
      this.prefs = {
        ...this.prefs,
        status: this.prefs.snap ? "ready" : "error",
        error: errorText(e),
      };
    }
    this.changed();
  }

  /** Takes a copy from the server if it is newer than the one here. */
  private async takePrefs(o: AccountPrefsObject | undefined, pushed: boolean) {
    try {
      const snap = await this.openPrefs(o);
      if (!this.prefs.snap || snap.revision >= this.prefs.snap.revision)
        this.keepPrefs(o);
      if (this.prefs.snap && snap.revision <= this.prefs.snap.revision) return;
      // Changed by another client while nothing of this one's is in flight.
      const elsewhere =
        pushed && this.prefs.pending.size === 0 && !!this.prefs.snap;
      this.prefs = {
        ...this.prefs,
        snap,
        status: "ready",
        changedAt: elsewhere ? Date.now() : this.prefs.changedAt,
      };
      this.changed();
    } catch (e) {
      if (!pushed) throw e;
      this.prefs = {
        ...this.prefs,
        status: this.prefs.snap ? "ready" : "error",
        error: errorText(e),
      };
      this.report(e);
    }
  }

  /** Pins, unpins, removes or restores a project, for the whole account.
   * Only while connected: offline the list is shown as cached and changes
   * wait. Resolves false if it was not saved; the list then stays as it
   * was, and retryPrefs tries the same operation again. */
  private settingsWrites = new Map<string, Promise<boolean>>();

  changePrefs(
    kind: PrefKind,
    path: string,
    op: PrefOp = { id: newRequestID(), kind, path, at: Date.now() },
  ): Promise<boolean> {
    if (kind !== "settings") return this.changePrefsNow(kind, path, op);
    const prior = this.settingsWrites.get(path);
    const next = prior
      ? prior.catch(() => false).then(() => this.changePrefsNow(kind, path, op))
      : this.changePrefsNow(kind, path, op);
    this.settingsWrites.set(path, next);
    void next.finally(() => {
      if (this.settingsWrites.get(path) === next)
        this.settingsWrites.delete(path);
    });
    return next;
  }

  private async changePrefsNow(
    kind: PrefKind,
    path: string,
    op: PrefOp,
  ): Promise<boolean> {
    if (this.conn !== "online" || this.prefs.status !== "ready") return false;
    this.prefs.pending.set(path, kind);
    this.prefs = { ...this.prefs, failed: undefined };
    this.changed();
    try {
      const snap = await commitOp(this.prefsStore, op, this.prefs.snap);
      if (!this.prefs.snap || snap.revision >= this.prefs.snap.revision)
        this.prefs = { ...this.prefs, snap };
      return true;
    } catch (e) {
      this.prefs = { ...this.prefs, failed: { op, error: errorText(e) } };
      return false;
    } finally {
      this.prefs.pending.delete(path);
      this.changed();
    }
  }

  /** Tries the failed operation again, as the same operation. */
  retryPrefs() {
    const f = this.prefs.failed;
    if (f) void this.changePrefs(f.op.kind, f.op.path, f.op);
  }

  // --- images ---

  /** Uploads an image to a machine for a message, in pieces that each fit
   * one command, and returns the image ID to send with it. After a reply
   * that was lost, it asks the machine how far it got and goes on from
   * there; the machine checks the length and hash at the end. */
  async uploadImage(
    machineId: string,
    p: { bytes: Uint8Array; format: string },
    progress: (done: number, total: number) => void = () => undefined,
    started: (abort: () => void) => void = () => undefined,
  ): Promise<{ imageId: string; expiresMs: number }> {
    const format =
      {
        jpeg: ImageFormat.JPEG,
        png: ImageFormat.PNG,
        webp: ImageFormat.WEBP,
        gif: ImageFormat.GIF,
      }[p.format] ?? ImageFormat.UNSPECIFIED;
    const transferId = newRequestID();
    const sha256 = new Uint8Array(
      await crypto.subtle.digest("SHA-256", p.bytes as Uint8Array<ArrayBuffer>),
    );
    const total = p.bytes.length;
    const statusOf = (r: CommandResult) => {
      const k = r.kind;
      const st =
        k.case === "beginImageUpload" ||
        k.case === "putImageChunk" ||
        k.case === "getImageUploadStatus" ||
        k.case === "completeImageUpload"
          ? k.value.status
          : undefined;
      if (!st) throw new Error("意外的回复");
      return st;
    };
    // A step whose outcome is unknown is followed by asking where it got.
    const step = async (cmd: CommandKind) => {
      for (let i = 0; ; i++) {
        try {
          return statusOf(await this.machineCommand(machineId, cmd));
        } catch (e) {
          if (!uncertain(e) || i >= 2) throw e;
          try {
            return statusOf(
              await this.machineCommand(machineId, {
                case: "getImageUploadStatus",
                value: { transferId },
              }),
            );
          } catch (e2) {
            if (!uncertain(e2) || i >= 2) throw e2;
          }
        }
      }
    };
    let st = await step({
      case: "beginImageUpload",
      value: { transferId, format, totalBytes: BigInt(total), sha256 },
    });
    // Dropped later (removed, 原图 switched, another machine): the machine
    // is told so rather than keeping it until it expires.
    started(
      () =>
        void this.machineCommand(machineId, {
          case: "abortImageUpload",
          value: { transferId },
        }).catch(() => undefined),
    );
    const chunk = Math.min(
      UPLOAD_CHUNK,
      st.maxChunkBytes > 0 ? st.maxChunkBytes : UPLOAD_CHUNK,
    );
    for (let guard = 0; Number(st.persistedBytes) < total; guard++) {
      if (guard > total / chunk + 10) throw new Error("上传没有进展");
      const offset = Number(st.persistedBytes);
      progress(offset, total);
      st = await step({
        case: "putImageChunk",
        value: {
          transferId,
          offset: BigInt(offset),
          data: p.bytes.subarray(offset, offset + chunk),
        },
      });
    }
    progress(total, total);
    st = await step({ case: "completeImageUpload", value: { transferId } });
    if (!st.imageId) throw new Error("电脑没有返回图片 ID");
    return { imageId: st.imageId, expiresMs: Number(st.expiresMs) };
  }

  // Display copies opened this visit, newest last, so a picture seen once
  // opens again offline. Only in memory; a few dozen at most.
  private imageCache = new Map<string, ImageResult>();

  /** The display copy of an image in a session: fetched, opened with the
   * session key where it belongs, and checked to be the format it says.
   * Throws when it cannot be fetched now (offline, network). */
  async getImage(sid: string, imageId: string): Promise<ImageResult> {
    const key = sid + "/" + imageId;
    const hit = this.imageCache.get(key);
    if (hit) {
      this.imageCache.delete(key);
      this.imageCache.set(key, hit);
      return hit;
    }
    const s = this.sessions.get(sid);
    if (!s) throw new Error("未知会话");
    let res;
    try {
      res = await this.api.getImage({ sessionId: sid, imageId });
    } catch (e) {
      if (e instanceof ConnectError && e.code === Code.NotFound)
        return { kind: "missing" };
      throw e;
    }
    if (res.expired || !res.image) return { kind: "expired" };
    let out: ImageResult;
    try {
      const { msg } = await this.openSecureMessage(
        res.image,
        {
          kind: PayloadKind.IMAGE,
          machineId: s.machineId,
          sessionId: sid,
          requestId: imageId,
        },
        ImagePayloadSchema,
        true,
      );
      out = matchesFormat(msg.data, msg.format)
        ? {
            kind: "ok",
            ...(fromBinary(HeaderSchema, res.image.header).version === 1
              ? { unverifiedLegacy: true }
              : {}),
            blob: new Blob([msg.data as Uint8Array<ArrayBuffer>], {
              type: mimeOf(msg.format),
            }),
            width: msg.width,
            height: msg.height,
            format: msg.format,
          }
        : { kind: "bad" };
    } catch (e) {
      this.report(e);
      out = { kind: "bad" };
    }
    if (out.kind === "ok") {
      this.imageCache.set(key, out);
      while (this.imageCache.size > IMAGE_CACHE)
        this.imageCache.delete(this.imageCache.keys().next().value!);
    }
    return out;
  }

  /** An image already opened this visit, without asking the server. */
  cachedImage(sid: string, imageId: string): ImageResult | undefined {
    return this.imageCache.get(sid + "/" + imageId);
  }

  /** Authorization for services called outside this class. */
  authHeader(): string {
    return "Bearer " + this.token;
  }

  async pushKey(): Promise<string> {
    return (await this.api.getPushKey({})).publicKey;
  }

  async setPushSubscription(
    subscriptionJson: string,
    kinds: NotifyKind[],
  ): Promise<void> {
    await this.api.setPushSubscription({ subscriptionJson, kinds });
  }

  private models = new Map<string, Promise<ListModelsResponse>>();
  private modelReads = new Set<string>();
  private modelLists = new Map<string, ListModelsResponse>();

  /** A model's display name, once the machine's list has been fetched. */
  modelName(machineId: string, agent: Agent, slug: string): string {
    return modelLabel(
      this.modelLists.get(machineId + "/" + agent)?.models,
      slug,
      agent,
    );
  }

  /** The model list last read from a machine's CLI, if any: what a page shows while it reads again. */
  cachedModels(
    machineId: string,
    agent: Agent,
  ): ListModelsResponse | undefined {
    return this.modelLists.get(machineId + "/" + agent);
  }

  /** The models a machine's CLI offers; pickers refresh completed reads. */
  listModels(
    machineId: string,
    agent: Agent = Agent.CODEX,
    fresh = false,
  ): Promise<ListModelsResponse> {
    const key = machineId + "/" + agent;
    let p =
      fresh && !this.modelReads.has(key) ? undefined : this.models.get(key);
    if (!p) {
      p = this.machineCommand(machineId, {
        case: "listModels",
        value: { agent },
      }).then((r) => {
        if (r.kind.case !== "listModels") throw new Error("意外的回复");
        this.modelLists.set(key, r.kind.value);
        this.keepInfo();
        this.changed();
        return r.kind.value;
      });
      this.modelReads.add(key);
      p.then(
        () => this.modelReads.delete(key),
        () => {
          this.modelReads.delete(key);
          if (this.models.get(key) === p) this.models.delete(key);
        },
      );
      this.models.set(key, p);
    }
    return p;
  }

  imageStorage = new Map<string, ImageStorageUsage>();

  private settingsCache = new Map<
    string,
    Promise<DaemonSettings | undefined>
  >();
  private settingsRead = new Map<string, DaemonSettings | undefined>();

  /** Keeps the model lists and settings read so far for the next visit: the
   * names of models and the CLIs' defaults, as the machines answered. */
  private keepInfo() {
    if (!this.cache) return;
    const models: Record<string, Uint8Array> = {};
    for (const [k, v] of this.modelLists)
      models[k] = toBinary(ListModelsResponseSchema, v);
    const settings: Record<string, Uint8Array> = {};
    for (const [k, v] of this.settingsRead)
      if (v) settings[k] = toBinary(DaemonSettingsSchema, v);
    this.keepValue("info", { models, settings });
  }

  /** The daemon settings last read from a machine, if any. */
  cachedSettings(machineId: string): DaemonSettings | undefined {
    return this.settingsRead.get(machineId);
  }

  /** A machine's daemon settings, fetched once per page load. */
  settings(
    machineId: string,
    fresh = false,
  ): Promise<DaemonSettings | undefined> {
    let p = fresh ? undefined : this.settingsCache.get(machineId);
    if (!p) {
      p = this.machineCommand(machineId, {
        case: "getSettings",
        value: {},
      }).then((r) => {
        if (r.kind.case !== "getSettings") throw new Error("意外的回复");
        if (r.kind.value.imageStorage)
          this.imageStorage.set(machineId, r.kind.value.imageStorage);
        this.settingsRead.set(machineId, r.kind.value.settings);
        this.keepInfo();
        this.changed();
        return r.kind.value.settings;
      });
      p.catch(() => this.settingsCache.delete(machineId));
      this.settingsCache.set(machineId, p);
    }
    return p;
  }

  async saveSettings(
    machineId: string,
    next: DaemonSettings,
    cleanImages = false,
  ): Promise<DaemonSettings | undefined> {
    const r = await this.machineCommand(machineId, {
      case: "updateSettings",
      value: { settings: next, cleanImages },
    });
    if (r.kind.case !== "updateSettings") throw new Error("意外的回复");
    if (r.kind.value.imageStorage)
      this.imageStorage.set(machineId, r.kind.value.imageStorage);
    this.changed();
    this.settingsCache.set(machineId, Promise.resolve(r.kind.value.settings));
    this.settingsRead.set(machineId, r.kind.value.settings);
    this.keepInfo();
    return r.kind.value.settings;
  }

  private report(e: unknown) {
    this.noteClock(e);
    console.warn("remote-agent:", errorText(e));
    this.error = errorText(e);
    this.changed();
  }
}

/** Request IDs of the messages recorded among events. */
function recordedIds(events: Event[]): string[] {
  return events.flatMap((e) =>
    e.kind.case === "userMessage" && e.kind.value.requestId
      ? [e.kind.value.requestId]
      : [],
  );
}

/** Whether an ended session comes after a cursor in the listing, which
 * runs from the latest ended to the earliest, then by session ID. */
function afterCursor(s: SessionView, c: SessionCursor): boolean {
  return (
    s.endedMs < c.endedMs || (s.endedMs === c.endedMs && s.id > c.sessionId)
  );
}

function maxBig(a: bigint, ...rest: bigint[]): bigint {
  return rest.reduce((m, x) => (x > m ? x : m), a);
}

export function errorText(e: unknown): string {
  if (e instanceof RecoveryHistoryUnavailable)
    return "历史密钥不可用：恢复前的记录无法解密";
  if (e instanceof SecurityError) {
    if (e.code === "lease")
      return "远程控制有效期已过，无法保存更改、控制会话或上传新内容；请在当前管理端续期后重试。历史仍可查看";
    if (e.code === "unavailable")
      return "等待所属电脑更新当前状态：这台设备没有当时的内容密钥";
    if (e.code === "invalid" || e.code === "untrusted")
      return "密文无效：签名、密钥或内容校验失败";
    if (e.code === "conflict")
      return "成员链冲突，安全操作已停止；请核对设备后明确重新加入";
  }
  if (e instanceof CommandError) return e.message;
  if (e instanceof ConnectError) {
    if (e.code === Code.PermissionDenied && e.rawMessage === "login failed")
      return "服务器不认识这台设备（可能已被移除，或服务已重建）";
    return e.rawMessage || e.message;
  }
  if (e instanceof Error) return e.message;
  return String(e);
}

function equalBytes(a: Uint8Array, b: Uint8Array): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i]);
}

/** Startup retries the exact signed request, including after a lost response. */
export async function resumeRegistration(): Promise<void> {
  const authority = await readSecurityOperation();
  if (authority?.kind === "create") throw new Error("账户创建草稿已保留，请继续原创建流程和恢复码确认");
  if (authority?.kind === "recovery") throw new Error("紧急恢复草稿已保留，请继续查询、等待或完成原恢复");
  const pending = await loadRegistration();
  if (!pending) return;
  const request = fromBinary(RegisterDeviceRequestSchema, pending.request);
  if (pending.security) {
    const registration = fromBinary(DeviceRegistrationSchema, request.registration);
    await saveAccountPublicProfile(pending.account.server, pending.security.pin,
      new Uint8Array(registration.accountPublicKey));
  }
  if (pending.security) {
    const r = pending.security;
    const { initializeSecurity } = await import("./device-security-store");
    await initializeSecurity(
      r.pin,
      r.states,
      r.proofs,
      r.memberID,
      r.xPrivate,
      pending.replace,
      r.lease,
    );
  }
  await saveAccount(pending.account);
  localStorage.setItem("ra-server", pending.account.server);
  const auth = createClient(
    AuthService,
    createConnectTransport({
      fetch: relayFetch,
      baseUrl: apiBase(pending.account.server),
      useBinaryFormat: true,
    }),
  );
  const res = await auth.registerDevice(
    request,
    { timeoutMs: 15000 },
  );
  if (res.deviceId !== pending.account.deviceId)
    throw new Error("server 返回了意外的设备 ID");
  const approval = fromBinary(
    PairApprovalSchema,
    fromBinary(RegisterDeviceRequestSchema, pending.request).approval,
  );
  const { clearPairingIdentity } = await import("./device-security-store");
  await clearPairingIdentity(pending.account.server, approval.pairingId);
  await clearRegistration(pending);
}
