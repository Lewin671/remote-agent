// Remote keeps the page in sync with the server: it logs in, holds the
// decrypted machines, sessions and events, follows the push stream, and
// sends sealed commands. Content is only ever decrypted here.

import { create, fromBinary, toBinary, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient, type Client, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { relayFetch } from "./transport";
import {
  accountKey,
  checkServer,
  concat,
  deriveKeys,
  deviceID,
  importRoot,
  importSessionKey,
  newDeviceKey,
  newRequestID,
  open,
  openMessage,
  parseSecret,
  REGISTER_CONTEXT,
  sealMessage,
  signEnvelope,
  signLogin,
  expect,
  type Keys,
} from "./crypto";
import { EnvelopeSchema, PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import { Agent, ImageFormat, InputStatus, SendMode, SessionStatus, type Event } from "./gen/remoteagent/v1/event_pb";
import { matchesFormat, mimeOf } from "./imageview";
import { SessionSchema, type ImageStorageUsage, type DaemonSettings, type ListModelsResponse, type Session } from "./gen/remoteagent/v1/local_pb";
import {
  AuthService,
  ClientService,
  DeviceKind,
  DeviceRegistrationSchema,
  ListMachinesResponseSchema,
  ListRemoteSessionsResponseSchema,
  NotifyKind,
  PeerFrameSchema,
  ServerFrameSchema,
  TermHelloSchema,
  type AccountPrefsObject,
  type Device,
  type Machine as RemoteMachine,
  type RemoteSession,
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
import { commitOp, emptyPrefs, type PrefKind, type PrefOp, type PrefsSnapshot, type PrefsStore } from "./prefs";
import { machineName, modelLabel, type Outgoing } from "./model";
import { eventCache, type CachedEvent, type EventCache } from "./eventcache";
import { PROTOCOL_VERSION } from "./protocol";
import { loadAccount, saveAccount, type StoredAccount } from "./storage";

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
  metaRev: bigint;
  lastSeq: bigint;
  updatedMs: bigint;
}

interface EventBuffer {
  events: Event[]; // contiguous from seq 1
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
  ) {
    super(message);
  }
}

/** Whether a command may or may not have run: a retry must then reuse
 * its request slot, so the daemon answers from its log rather than
 * running it again. A definite answer (done, refused, never sent) lets
 * the next attempt start afresh. */
export function uncertain(e: unknown): boolean {
  if (e instanceof CommandError) return e.code === "unknown" || e.code === "aborted";
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

const noAnswer: AnswerState = { index: 0, ticked: [], busy: "", error: "", unknown: false };

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

export const COMMAND_TTL_MS = 60_000;

/** Largest piece of an image upload: each is sealed into one command,
 * which must stay under 1 MiB. */
const UPLOAD_CHUNK = 768 * 1024;

/** How many opened display copies stay in memory. */
const IMAGE_CACHE = 40;

/** A display copy fetched with GetImage: shown only when it opened where
 * it belongs and is the format it says. */
export type ImageResult =
  | { kind: "ok"; blob: Blob; width: number; height: number; format: ImageFormat }
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

/** Joins the account in secretText with a new browser device. */
export async function join(secretText: string): Promise<StoredAccount> {
  const secret = parseSecret(secretText);
  return joinWithRoot(secret.root, secret.server);
}

/** Registers this browser as a device of the account with root key root. */
export async function joinWithRoot(rootRaw: Uint8Array<ArrayBuffer>, server: string): Promise<StoredAccount> {
  server = checkServer(server);
  const root = await importRoot(rootRaw);
  const acct = await accountKey(root);
  const device = await newDeviceKey();
  const pub = new Uint8Array(await crypto.subtle.exportKey("raw", device.publicKey));
  const reg = toBinary(
    DeviceRegistrationSchema,
    create(DeviceRegistrationSchema, {
      accountPublicKey: acct.publicKey,
      devicePublicKey: pub,
      kind: DeviceKind.CLIENT,
      createdMs: BigInt(Date.now()),
    }),
  );
  const auth = createClient(AuthService, createConnectTransport({ fetch: relayFetch, baseUrl: apiBase(server), useBinaryFormat: true }));
  const res = await auth.registerDevice({ registration: reg, signature: acct.sign(concat(new TextEncoder().encode(REGISTER_CONTEXT), reg)) });
  if (res.deviceId !== (await deviceID(pub))) throw new Error("server 返回了意外的设备 ID");
  const stored: StoredAccount = { server, root, rootRaw: new Uint8Array(rootRaw), devicePrivate: device.privateKey, deviceId: res.deviceId };
  await saveAccount(stored);
  localStorage.setItem("ra-server", server);
  return stored;
}

/** How long the stream may take from dialing to the server's ready. */
const OPEN_TIMEOUT_MS = 15_000;

export class Remote {
  /** Development fixture hooks; the real client never enables these. */
  demoFlag(_name: string): boolean { return false; }
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
  failures = 0;
  connError = "";
  // When the link to the server last went down, while it is down: what is
  // shown as running may have changed since, so clocks stop there.
  lostAt?: number;
  // Set once machines and sessions have been listed; listError says why
  // the last listing failed.
  listed = false;
  listError = "";
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
  // What the user has typed and not sent, by session. Once sent, the
  // message is in the outbox.
  drafts = new Map<string, string>();
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
  prepareImage: Prepare = async (file, original) => (await import("./sendimages")).prepareForSending(file, original);
  // Cancels asked for from this page, by the queued message's request ID,
  // until the message leaves the queue.
  cancels = new Map<string, { state: "canceling" | "failed"; error: string }>();
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
      if (n) this.historyUsers.set(sid, n); else this.historyUsers.delete(sid);
      const b = this.buffers.get(sid);
      if (b) b.usedAt = Date.now();
      this.trimHistory();
    };
  }

  private trimHistory() {
    clearTimeout(this.historyTrim);
    const idle = [...this.buffers].filter(([sid, b]) => !this.historyUsers.has(sid) && !this.outbox(sid).some((o) => {
      const status = b.inputStatuses?.get(o.sendId);
      const finished = this.sessions.get(sid)?.info?.status === SessionStatus.EXITED ||
        (status !== undefined && [InputStatus.CANCELED, InputStatus.FAILED, InputStatus.NEEDS_TERMINAL, InputStatus.UNCONFIRMED].includes(status));
      return o.status === "sending" || (o.status === "sent" && !!o.thumbs?.length && !finished);
    }))
      .sort((a, b) => (a[1].usedAt ?? 0) - (b[1].usedAt ?? 0));
    let bytes = idle.reduce((n, [, b]) => n + (b.bytes ?? 0), 0);
    let count = idle.length;
    const now = Date.now();
    for (const [sid, b] of idle) {
      if (!this.sessions.has(sid) || count > 8 || bytes > 16 * 1024 * 1024 || now - (b.usedAt ?? now) >= 60_000) {
        this.buffers.delete(sid);
        count--; bytes -= b.bytes ?? 0;
      }
    }
    // Deferred trimming also catches histories read once without a mounted consumer.
    if (!this.closed && this.buffers.size) this.historyTrim = setTimeout(() => this.trimHistory(), 60_000);
  }
  // Pushes seen per machine, session and machine removal, so that a list
  // fetched before a push cannot undo it.
  private machinePushes = new Map<string, number>();
  private sessionPushes = new Map<string, number>();
  private removedPushes = new Map<string, number>();
  private token = "";
  private tokenExpires = 0;
  private loggingIn?: Promise<void>;
  private api: Client<typeof ClientService>;
  private ws?: WebSocket;
  private retry = 1000;
  private closed = false;
  private generation = 0;
  private retryTimer?: ReturnType<typeof setTimeout>;
  private openTimer?: ReturnType<typeof setTimeout>;
  private terminals = new Set<WebSocket>();
  private listeners = new Set<() => void>();
  private version = 0;

  protected constructor(
    readonly account: StoredAccount,
    private keys: Keys,
    private cache?: EventCache,
  ) {
    const auth: Interceptor = (next) => (req) => {
      req.header.set("Authorization", "Bearer " + this.token);
      return next(req);
    };
    this.api = createClient(ClientService, createConnectTransport({ fetch: relayFetch, baseUrl: apiBase(account.server), useBinaryFormat: true, interceptors: [auth] }));
  }

  static async load(): Promise<Remote | undefined> {
    const acct = await loadAccount();
    if (!acct) return undefined;
    return new Remote(acct, await deriveKeys(acct.root), eventCache());
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

  protected changed() {
    this.version++;
    if (this.held) this.dirty = true;
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
  // machines and sessions as last listed, shown until the server answers.
  private warming?: Promise<void>;

  private warm(): Promise<void> {
    return (this.warming ??= (async () => {
      if (!this.cache) return;
      const mine = (v: unknown): Record<string, unknown> | undefined => {
        const o = v as Record<string, unknown> | undefined;
        return o && o.deviceId === this.account.deviceId && o.server === this.account.server ? o : undefined;
      };
      try {
        const [token, machines, sessions] = (await Promise.all(["token", "machines", "sessions"].map((k) => this.cache!.get(k)))).map(mine);
        if (token && !this.token) [this.token, this.tokenExpires] = [String(token.token), Number(token.expiresMs)];
        if (machines && sessions && !this.listed)
          await this.quietly(async () => {
            for (const m of fromBinary(ListMachinesResponseSchema, machines.bytes as Uint8Array).machines) await this.putMachine(m);
            for (const s of fromBinary(ListRemoteSessionsResponseSchema, sessions.bytes as Uint8Array).sessions) await this.putSession(s);
          });
      } catch {
        // Nothing kept, or not readable: the server has it all.
      }
    })());
  }

  private keepValue(name: string, value: Record<string, unknown> | undefined) {
    void this.cache?.set(name, value && { ...value, deviceId: this.account.deviceId, server: this.account.server }).catch(() => undefined);
  }

  // --- connection ---

  start() {
    if (this.removed || (!this.closed && this.ws)) return;
    this.closed = false;
    this.trimHistory();
    void this.connect();
  }

  stop() {
    this.closed = true;
    clearTimeout(this.historyTrim);
    this.historyTrim = undefined;
    this.generation++;
    clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    clearTimeout(this.openTimer);
    this.ws?.close();
    this.ws = undefined;
    for (const terminal of this.terminals) terminal.close();
    this.terminals.clear();
    if (this.conn === "online") this.lostAt = Date.now();
    this.conn = "offline";
    this.changed();
  }

  /** Connects now instead of waiting out the backoff, e.g. when the
   * network came back. */
  reconnectNow() {
    if (this.closed || this.removed || this.ws || this.retryTimer === undefined) return;
    clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    this.retry = 1000;
    void this.connect();
  }

  /** One login at a time: the server keeps few challenges per device, and
   * a denial has to mean what it says. */
  private login(): Promise<void> {
    return (this.loggingIn ??= this.signIn().finally(() => (this.loggingIn = undefined)));
  }

  private async signIn() {
    const auth = createClient(AuthService, createConnectTransport({ fetch: relayFetch, baseUrl: apiBase(this.account.server), useBinaryFormat: true }));
    for (let again = true; ; again = false) {
      try {
        const ch = await auth.getChallenge({ deviceId: this.account.deviceId });
        const sig = await signLogin(this.account.devicePrivate, ch.challenge);
        const res = await auth.login({ deviceId: this.account.deviceId, challenge: ch.challenge, signature: sig });
        this.token = res.token;
        this.tokenExpires = Number(res.expiresMs);
        this.keepValue("token", { token: res.token, expiresMs: Number(res.expiresMs) });
        return;
      } catch (e) {
        if (!(e instanceof ConnectError) || e.code !== Code.PermissionDenied) throw e;
        // A server restarted between the two calls forgets the challenge;
        // denied a fresh one too, it no longer knows this device.
        if (again) continue;
        this.keepValue("token", undefined);
        this.removed = true;
        this.stop();
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
    this.changed();
    if (this.cache) {
      await this.warm();
      if (this.closed || generation !== this.generation) return;
    }
    try {
      // A token kept from an earlier visit saves the two login calls. One
      // about to expire is not worth a refused connection; the server
      // refusing it closes the stream, and the next try logs in.
      if (!this.token || this.tokenExpires < Date.now() + 86_400_000) await this.login();
    } catch (e) {
      if (generation === this.generation && !this.closed) this.fail(e);
      return;
    }
    if (generation !== this.generation || this.closed) return;
    const url = apiBase(this.account.server).replace(/^http/, "ws") + "/v1/stream";
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    // A browser gives up on a handshake whose packets are lost only after
    // minutes; the next attempt usually gets through much sooner.
    clearTimeout(this.openTimer);
    this.openTimer = setTimeout(() => {
      if (this.ws === ws && this.conn !== "online") ws.close();
    }, OPEN_TIMEOUT_MS);
    ws.onopen = () => {
      const hello = create(PeerFrameSchema, { kind: { case: "hello", value: { token: this.token } } });
      ws.send(toBinary(PeerFrameSchema, hello));
    };
    let processing = Promise.resolve();
    let queuedBytes = 0;
    let queuedFrames = 0;
    let overflow = false;
    ws.onmessage = (m) => {
      if (this.closed || generation !== this.generation || overflow) return;
      const data = m.data as ArrayBuffer;
      queuedBytes += data.byteLength;
      queuedFrames++;
      if (queuedBytes > 8 * 1024 * 1024 || queuedFrames > 1024) {
        overflow = true;
        ws.close();
        return;
      }
      processing = processing.then(async () => {
        if (this.closed || generation !== this.generation || overflow) return;
        let f;
        try {
          f = fromBinary(ServerFrameSchema, new Uint8Array(data));
        } catch {
          overflow = true;
          ws.close();
          return;
        }
        await this.onFrame(f.kind, generation);
      }).catch((e) => { this.report(e); ws.close(); })
        .finally(() => { queuedBytes -= data.byteLength; queuedFrames--; });
    };
    ws.onclose = (e) => {
      if (this.ws !== ws) return;
      this.ws = undefined;
      clearTimeout(this.openTimer);
      // The server refused the hello: the token is why. A handshake the
      // network dropped says nothing about it, and keeps the login.
      if (e?.code === 1008) this.token = "";
      this.fail(new Error("与 server 的连接断开"));
    };
  }

  private fail(e: unknown) {
    if (this.conn === "online") this.lostAt = Date.now();
    this.conn = "offline";
    this.failures++;
    this.connError = errorText(e);
    this.error = errorText(e);
    this.changed();
    if (this.closed) return;
    const delay = this.retry;
    this.retry = Math.min(this.retry * 2, 30_000);
    clearTimeout(this.retryTimer);
    this.retryTimer = setTimeout(() => void this.connect(), delay + Math.random() * delay * 0.25);
  }

  private async onFrame(kind: ReturnType<typeof fromBinary<typeof ServerFrameSchema>>["kind"], generation = this.generation) {
    if (this.closed || generation !== this.generation) return;
    switch (kind.case) {
      case "ready":
        clearTimeout(this.openTimer);
        this.retry = 1000;
        this.conn = "online";
        this.lostAt = undefined;
        this.everOnline = true;
        this.failures = 0;
        this.connError = "";
        this.error = "";
        this.changed();
        // Subscribed first, then catch up: nothing falls in between.
        await this.refresh();
        if (this.closed || generation !== this.generation) return;
        for (const sid of this.buffers.keys()) void this.fetchEvents(sid);
        void this.loadDevices();
        void this.loadPrefs();
        break;
      case "accountPrefsUpdated":
        if (kind.value.object) await this.takePrefs(kind.value.object, true);
        break;
      case "deviceUpdated":
        if (kind.value.device) {
          await this.putDevice(kind.value.device);
          this.changed();
        }
        break;
      case "deviceRemoved":
        this.deviceRemovals.set(kind.value.deviceId, (this.deviceRemovals.get(kind.value.deviceId) ?? 0) + 1);
        this.devices.delete(kind.value.deviceId);
        this.changed();
        break;
      case "machineRemoved":
        this.bump(this.removedPushes, kind.value.machineId);
        this.machines.delete(kind.value.machineId);
        for (const [id, s] of this.sessions) if (s.machineId === kind.value.machineId) this.sessions.delete(id);
        this.changed();
        break;
      case "machineUpdated":
        if (kind.value.machine) {
          const id = kind.value.machine.id;
          this.bump(this.machinePushes, id);
          await this.putMachine(kind.value.machine);
        }
        break;
      case "sessionUpdated":
        if (kind.value.session) {
          this.bump(this.sessionPushes, kind.value.session.sessionId);
          await this.putSession(kind.value.session);
        }
        break;
      case "eventsAppended": {
        const sid = kind.value.sessionId;
        const s = this.sessions.get(sid);
        if (!s || !this.buffers.has(sid)) {
          if (s) s.lastSeq = maxBig(s.lastSeq, ...kind.value.events.map((e) => e.seq));
          this.changed();
          break;
        }
        await this.ingest(s, kind.value.events);
        break;
      }
    }
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
      const pushed = (now: Map<string, number>, then: Map<string, number>, id: string) => (now.get(id) ?? 0) !== (then.get(id) ?? 0);
      const [ms, ss] = await Promise.all([this.api.listMachines({}), this.api.listRemoteSessions({})]);
      // Forget what the server no longer has, e.g. a machine another
      // device removed while this one was offline.
      const machines = new Set(ms.machines.map((m) => m.id));
      const sessions = new Set(ss.sessions.map((s) => s.sessionId));
      for (const id of this.machines.keys()) if (!machines.has(id) && !pushed(this.machinePushes, mp, id)) this.machines.delete(id);
      for (const id of this.sessions.keys()) if (!sessions.has(id) && !pushed(this.sessionPushes, sp, id)) this.sessions.delete(id);
      const removed = (id: string) => pushed(this.removedPushes, rp, id);
      await this.quietly(async () => {
        for (const m of ms.machines) {
          if (removed(m.id)) continue;
          await this.putMachine(m, pushed(this.machinePushes, mp, m.id));
        }
        for (const s of ss.sessions) if (!removed(s.machineId)) await this.putSession(s);
      });
      if (this.cache) {
        this.keepValue("machines", { bytes: toBinary(ListMachinesResponseSchema, ms) });
        this.keepValue("sessions", { bytes: toBinary(ListRemoteSessionsResponseSchema, ss) });
      }
      this.listed = true;
      this.listError = "";
      this.listRetry = 2000;
      void this.cache?.keep(sessions).catch(() => undefined);
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
    }
  }

  /** Lists machines and sessions again now, after a failure. */
  retryList() {
    clearTimeout(this.listTimer);
    this.listError = "";
    this.changed();
    void this.refresh();
  }

  /** Stores a machine. staleOnline: a push arrived after m was fetched,
   * so its online flag is out of date. */
  private async putMachine(m: RemoteMachine, staleOnline = false) {
    let view = this.machines.get(m.id);
    if (!view) {
      view = { id: m.id, online: m.online, lastSeenMs: m.lastSeenMs, metaRev: 0n };
      this.machines.set(m.id, view);
    }
    if (!staleOnline) view.online = m.online;
    if (m.lastSeenMs > view.lastSeenMs) view.lastSeenMs = m.lastSeenMs;
    if (m.meta) {
      try {
        const { header, msg } = await openMessage(this.keys.meta, m.meta, { kind: PayloadKind.MACHINE_META, machineId: m.id }, MachineMetaSchema);
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
    let view = this.sessions.get(rs.sessionId);
    const removals = this.removedPushes.get(rs.machineId) ?? 0;
    try {
      if (!view) {
        const { header, plaintext } = await open(this.keys.wrap, rs.key);
        expect(header, { kind: PayloadKind.SESSION_KEY, machineId: rs.machineId, sessionId: rs.sessionId });
        const key = await importSessionKey(plaintext as Uint8Array<ArrayBuffer>);
        // The machine may have been removed while the key was opened.
        if ((this.removedPushes.get(rs.machineId) ?? 0) !== removals) return;
        view = this.sessions.get(rs.sessionId) ?? {
          id: rs.sessionId,
          machineId: rs.machineId,
          key,
          metaRev: 0n,
          lastSeq: 0n,
          updatedMs: 0n,
        };
        this.sessions.set(rs.sessionId, view);
      }
      if (view.machineId !== rs.machineId) throw new Error("会话记录的机器被改动");
      view.lastSeq = maxBig(view.lastSeq, rs.lastSeq);
      view.updatedMs = maxBig(view.updatedMs, rs.updatedMs);
      if (rs.meta) {
        const { header, msg } = await openMessage(
          view.key,
          rs.meta,
          { kind: PayloadKind.SESSION_META, machineId: rs.machineId, sessionId: rs.sessionId },
          SessionSchema,
        );
        if (header.seq >= view.metaRev) {
          view.info = msg;
          view.metaRev = header.seq;
        }
      }
    } catch (e) {
      this.report(e);
    }
    this.changed();
  }

  // --- events ---

  /** Events of a session, contiguous from seq 1. Loads them on first use. */
  events(sid: string): Event[] {
    const b = this.buffers.get(sid);
    if (b) b.usedAt = Date.now();
    if (!b) {
      this.buffers.set(sid, { events: [], pending: new Map(), fetching: false, refetch: false, loaded: false, restored: false, error: "", broken: false, bytes: 0, usedAt: Date.now() });
      void this.fetchEvents(sid);
      return [];
    }
    return b.events;
  }

  loaded(sid: string): boolean {
    return this.buffers.get(sid)?.loaded ?? false;
  }

  history(sid: string): History {
    const b = this.buffers.get(sid);
    return { loaded: b?.loaded ?? false, error: b?.error ?? "", broken: b?.broken ?? false };
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
    const rows = await this.cache.read(s.id).catch((): CachedEvent[] => []);
    if (!rows.length || stale()) return;
    if (BigInt(rows.length) <= s.lastSeq) {
      await this.ingest(s, rows, false, true);
      if (stale() || !b.broken) return;
      Object.assign(b, { events: [], pending: new Map(), broken: false, bytes: 0, inputStatuses: undefined });
    }
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
    try {
      if (!b.restored && this.cache) await this.restore(s, b);
      do {
        b.refetch = false;
        for (;;) {
          if (this.closed || generation !== this.generation || this.buffers.get(sid) !== b) return;
          const before = b.events.length;
          const res = await this.api.getEvents({ sessionId: sid, afterSeq: BigInt(before), limit: 500 });
          if (this.closed || generation !== this.generation || this.buffers.get(sid) !== b) return;
          await this.ingest(s, res.events, false);
          // An event that does not open stops progress; asking for the
          // same page again would not help.
          if (!res.more || b.events.length === before) break;
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
      if (b.refetch && !this.closed && generation !== this.generation) void this.fetchEvents(sid);
      this.trimHistory();
      this.changed();
    }
  }

  private async ingest(s: SessionView, stored: StoredEvent[] | CachedEvent[], fillGaps = true, cached = false) {
    const b = this.buffers.get(s.id);
    if (!b) return;
    const fresh: CachedEvent[] = [];
    const save = () => {
      if (fresh.length && !cached) void this.cache?.append(s.id, fresh).catch(() => undefined);
    };
    for (const se of stored) {
      if (se.seq <= BigInt(b.events.length) || b.pending.has(se.seq)) continue;
      try {
        const { msg } = await openMessage(
          s.key,
          se.envelope,
          { kind: PayloadKind.EVENT, machineId: s.machineId, sessionId: s.id, seq: se.seq },
          EventSchema,
        );
        if (this.buffers.get(s.id) !== b) return;
        if (msg.seq !== se.seq || msg.sessionId !== s.id) throw new Error("事件的位置被改动");
        b.pending.set(se.seq, msg);
        b.bytes = (b.bytes ?? 0) + se.envelope!.ciphertext.byteLength + 256;
        fresh.push({ seq: se.seq, envelope: se.envelope! });
      } catch (e) {
        b.broken = true;
        save();
        this.report(e);
        return;
      }
    }
    save();
    if (this.buffers.get(s.id) !== b) return;
    for (let next = BigInt(b.events.length + 1); b.pending.has(next); next++) {
      const event = b.pending.get(next)!;
      if (event.kind.case === "inputState") {
        (b.inputStatuses ??= new Map()).set(event.kind.value.requestId, event.kind.value.status);
      }
      b.events.push(event);
      b.pending.delete(next);
    }
    s.lastSeq = maxBig(s.lastSeq, BigInt(b.events.length));
    this.recorded(s.id, stored.length ? recordedIds(b.events.slice(-stored.length)) : []);
    if (fillGaps && b.pending.size > 0) void this.fetchEvents(s.id);
    this.changed();
  }

  // --- commands ---

  /** Sends a command about a session. slot, when given, is the request
   * ID and deadline of an earlier attempt of the same command. */
  async command(sid: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> {
    const s = this.sessions.get(sid);
    if (!s) throw new Error("未知会话");
    return this.forward(s.machineId, sid, cmd, slot);
  }

  /** Sends a command about a machine, such as starting a session. */
  machineCommand(machineId: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> {
    return this.forward(machineId, "", cmd, slot);
  }

  /** Seals cmd, forwards it and returns the opened result. Network
   * failures are retried with the same request ID, which the daemon
   * runs at most once. */
  private async forward(machineId: string, sid: string, cmd: CommandKind, slot = newSlot()): Promise<CommandResult> {
    const { requestId, deadline } = slot;
    // A machine of another protocol version would refuse the command, in
    // words that do not say which side to update: say so here, unsent.
    const m = this.machines.get(machineId);
    const theirs = m?.meta?.protocolVersion;
    if (theirs !== undefined && theirs !== PROTOCOL_VERSION)
      throw new CommandError(
        "failed_precondition",
        theirs < PROTOCOL_VERSION ? `${machineName(m)} 上的 remote-agent 版本较旧，请在电脑上更新后再试` : "这个网页版本较旧，请刷新或更新后再试",
      );
    // Signed by this device, so the daemon knows who sent it whatever the
    // server says (the server also checks the sender is the caller).
    const env = await signEnvelope(
      this.account.devicePrivate,
      await sealMessage(
        this.keys.command,
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
      ),
    );
    // Once a try may have reached the daemon, no later failure of this
    // request can say it did not run.
    let mayHaveRun = false;
    for (let attempt = 0; ; attempt++) {
      try {
        const res = await this.api.forward({ machineId, command: env });
        const { msg } = await openMessage(
          this.keys.command,
          res.result,
          { kind: PayloadKind.COMMAND_RESULT, machineId, sessionId: sid, requestId },
          CommandResultSchema,
        );
        if (msg.errorCode) throw new CommandError(msg.errorCode, msg.errorMessage);
        return msg;
      } catch (e) {
        // The daemon's answer is final; only transport failures are retried.
        if (!(e instanceof ConnectError)) throw e;
        const ce = e;
        if (ce.code === Code.Unauthenticated && attempt < 3) {
          await this.login();
          continue;
        }
        // DeadlineExceeded is also the server's word for a daemon that gave
        // no result: it may have run.
        const unknown = [Code.DeadlineExceeded, Code.Unknown, Code.Internal, Code.Canceled].includes(ce.code);
        if (unknown) mayHaveRun = true;
        if (unknown && attempt < 3 && Date.now() + 2000 < deadline) {
          await new Promise((r) => setTimeout(r, 1000 * (attempt + 1)));
          continue;
        }
        if (ce.code === Code.Unavailable)
          throw mayHaveRun ? new CommandError("unknown", "电脑断开了，不确定指令是否已经执行") : new CommandError("unavailable", "电脑不在线，指令没有发出");
        if (unknown) throw new CommandError("unknown", "连接超时");
        throw e;
      }
    }
  }

  /** Sends a chat message. A retry passes the first attempt's slot, so
   * the daemon answers it from its log. Only if that slot expired before
   * reaching the computer is a new one used; the send ID still keeps the
   * daemon from queueing the text twice. Returns the slot that got an
   * answer. */
  async sendMessage(sid: string, text: string, sendId: string, slot: RequestSlot, wasUnknown = false, imageIds: string[] = [], mode = SendMode.STEER): Promise<RequestSlot> {
    const cmd: CommandKind = { case: "send", value: { sessionId: sid, text, requestId: sendId, imageIds, mode } };
    try {
      await this.command(sid, cmd, slot);
      return slot;
    } catch (e) {
      // A send whose outcome was unknown is never sealed afresh.
      if (wasUnknown || !(e instanceof CommandError) || e.code !== "deadline_exceeded" || Date.now() < slot.deadline) throw e;
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
    this.outboxes.set(
      sid,
      this.outbox(sid).map((o) => (o.sendId === sendId ? { ...o, ...patch } : o)),
    );
    this.changed();
  }

  /** Sends text, and images uploaded to the machine, to a session; its
   * progress shows in outbox(sid). */
  send(sid: string, text: string, imageIds: string[] = [], thumbs: Blob[] = [], mode = SendMode.STEER): string {
    const o: Outgoing = { sendId: newRequestID(), ...newSlot(), text, imageIds, thumbs, mode, status: "sending" };
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
  resend(sid: string, failedId: string, text: string, imageIds: string[] = [], thumbs: Blob[] = []) {
    if (this.replaced.has(failedId)) return;
    this.replaced.set(failedId, this.send(sid, text, imageIds, thumbs));
  }

  /** The CLI recorded these messages: their images are in the session
   * now, so this device's copies (up to six originals each) are let go.
   * Messages not recorded keep theirs, to be put back into the input. */
  recorded(sid: string, requestIds: string[]) {
    if (!requestIds.length) return;
    const ids = new Set(requestIds);
    const box = this.outbox(sid);
    if (!box.some((o) => ids.has(o.sendId) && o.thumbs?.length)) return;
    this.outboxes.set(
      sid,
      box.map((o) => (ids.has(o.sendId) && o.thumbs ? { ...o, thumbs: undefined } : o)),
    );
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
    this.patchOut(sid, o.sendId, { status: "sending", error: undefined });
    try {
      const slot = await this.sendMessage(sid, o.text, o.sendId, { requestId: o.requestId, deadline: o.deadline }, o.unknown, o.imageIds, o.mode);
      this.patchOut(sid, o.sendId, { status: "sent", ...slot });
    } catch (e) {
      // Once unknown, it stays unknown and keeps its request (attempt.ts);
      // the send ID also keeps the daemon from queueing the text twice.
      const still = stillUnknown(e, o.unknown ?? false);
      this.patchOut(sid, o.sendId, { status: "failed", error: errorText(e), unknown: still, ...(still ? {} : newSlot()) });
    }
  }

  /** Joins a session's terminal stream. Frames are sealed with the session
   * key and numbered; each side refuses frames out of order. */
  async openTerminal(sid: string, onFrame: (f: TerminalFrame) => void | Promise<void>, onClose: (reason: string) => void): Promise<TermConn> {
    const s = this.sessions.get(sid);
    if (!s) throw new Error("未知会话");
    const streamId = newRequestID();
    const ws = new WebSocket(apiBase(this.account.server).replace(/^http/, "ws") + "/v1/term");
    ws.binaryType = "arraybuffer";
    this.terminals.add(ws);
    let closed = false;
    const finish = (reason: string) => {
      if (closed) return;
      closed = true;
      this.terminals.delete(ws);
      onClose(reason);
    };
    await new Promise<void>((resolve, reject) => {
      ws.onopen = () => {
        ws.send(toBinary(TermHelloSchema, create(TermHelloSchema, { token: this.token, streamId, machineId: s.machineId })));
        resolve();
      };
      ws.onerror = () => { this.terminals.delete(ws); ws.close(); reject(new Error("连不上终端")); };
      ws.onclose = () => { this.terminals.delete(ws); closed = true; reject(new Error("终端连接已关闭")); };
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
      if (pendingBytes > 2 * 1024 * 1024 || pendingFrames > 512) { finish("终端输出积压，请重新连接以获取最新画面"); ws.close(); return; }
      // Open in arrival order.
      queue = queue.then(async () => {
        try {
          if (closed) return;
          const env = fromBinary(EnvelopeSchema, new Uint8Array(m.data as ArrayBuffer));
          const { header, msg } = await openMessage(
            s.key,
            env,
            { kind: PayloadKind.TERMINAL_OUTPUT, machineId: s.machineId, sessionId: sid, requestId: streamId },
            TerminalFrameSchema,
          );
          if (closed) return;
          if (header.seq <= last) throw new Error("终端帧顺序不对");
          last = header.seq;
          await onFrame(msg);
        } catch (e) {
          finish(errorText(e));
          ws.close();
        } finally { pendingBytes -= size; pendingFrames--; }
      });
    };
    ws.onclose = () => finish("终端连接断开");
    try {
      await this.command(sid, { case: "openTerminal", value: { sessionId: sid, streamId } });
    } catch (e) {
      this.terminals.delete(ws);
      ws.close();
      throw e;
    }
    let seq = 0n;
    let sending = Promise.resolve();
    return {
      send: (frame) => {
        sending = sending.then(async () => {
          if (closed || ws.readyState !== WebSocket.OPEN) return;
          seq++;
          const env = await sealMessage(
            s.key,
            { kind: PayloadKind.TERMINAL_INPUT, machineId: s.machineId, sessionId: sid, requestId: streamId, seq, sender: this.account.deviceId },
            TerminalFrameSchema,
            create(TerminalFrameSchema, frame),
          );
          ws.send(toBinary(EnvelopeSchema, env));
        });
      },
      close: () => {
        closed = true;
        this.terminals.delete(ws);
        ws.close();
      },
    };
  }

  // --- devices ---

  /** Lists the account's devices. */
  async loadDevices() {
    try {
      const r = await this.api.listDevices({});
      const ids = new Set(r.devices.map((d) => d.id));
      for (const id of this.devices.keys()) if (!ids.has(id)) this.devices.delete(id);
      for (const d of r.devices) await this.putDevice(d);
      this.devicesStatus = "ready";
      this.changed();
      await this.describeSelf();
    } catch (e) {
      this.devicesStatus = "error";
      this.changed();
    }
  }

  // Removals seen per device, so that metadata still opening when its
  // device goes does not bring it back.
  private deviceRemovals = new Map<string, number>();

  private async putDevice(d: Device) {
    const removals = this.deviceRemovals.get(d.id) ?? 0;
    let meta: DeviceMeta | undefined;
    if (d.meta) {
      try {
        ({ msg: meta } = await openMessage(this.keys.deviceMeta, d.meta, { kind: PayloadKind.DEVICE_META, requestId: d.id, seq: d.metaRevision }, DeviceMetaSchema));
      } catch (e) {
        this.report(e);
      }
    }
    // Checked after opening, against what is stored now: another update
    // may have landed, or the device gone, in the meantime.
    if ((this.deviceRemovals.get(d.id) ?? 0) !== removals) return;
    const cur = this.devices.get(d.id);
    const info: DeviceInfo = { id: d.id, kind: d.kind, createdMs: d.createdMs, metaRev: cur?.metaRev ?? 0n, meta: cur?.meta, lastSeenMs: d.lastSeenMs };
    if (meta && d.metaRevision >= info.metaRev) {
      info.meta = meta;
      info.metaRev = d.metaRevision;
    }
    this.devices.set(d.id, info);
  }

  /** Says what this browser is, once, so other clients can name it. */
  private async describeSelf() {
    const me = this.devices.get(this.account.deviceId);
    if (!me || me.meta) return;
    await this.changeDeviceMeta(this.account.deviceId, (m) => ({ name: m?.name ?? "", platform: "web", description: m?.description || describeBrowser(navigator.userAgent) })).catch((e) => this.report(e));
  }

  /** Renames a client; the name is sealed, the server sees none. */
  renameDevice(id: string, name: string): Promise<void> {
    return this.changeDeviceMeta(id, (m) => ({ name: name.trim(), platform: m?.platform ?? "", description: m?.description ?? "" }));
  }

  /** Replaces a device's metadata with change(current). If another client
   * wrote first, the change is made again on what it wrote. */
  private async changeDeviceMeta(id: string, change: (m: DeviceMeta | undefined) => Omit<Partial<DeviceMeta>, "$typeName">) {
    let cur = this.devices.get(id);
    for (let i = 0; i < 5; i++) {
      const rev = cur?.metaRev ?? 0n;
      const env = await sealMessage(this.keys.deviceMeta, { kind: PayloadKind.DEVICE_META, requestId: id, seq: rev + 1n, sender: this.account.deviceId }, DeviceMetaSchema, create(DeviceMetaSchema, change(cur?.meta)));
      const r = await this.api.putDeviceMeta({ deviceId: id, expectedRevision: rev, meta: env });
      if (r.device) await this.putDevice(r.device);
      this.changed();
      if (r.applied) return;
      cur = this.devices.get(id);
    }
    throw new Error("设备信息一直在被别的客户端修改，没能保存");
  }

  // --- project list preferences ---

  protected prefsStore: PrefsStore = {
    get: async () => {
      const r = await this.api.getAccountPrefs({ objectId: PREFS_OBJECT });
      return this.openPrefs(r.object);
    },
    put: async (expected, next) => {
      const env = await sealMessage(this.keys.prefs, { kind: PayloadKind.ACCOUNT_PREFS, requestId: PREFS_OBJECT, seq: expected + 1n, sender: this.account.deviceId }, ProjectPrefsSchema, next);
      const r = await this.api.putAccountPrefs({ objectId: PREFS_OBJECT, expectedRevision: expected, envelope: env });
      return { applied: r.applied, current: await this.openPrefs(r.object) };
    },
  };

  private async openPrefs(o: AccountPrefsObject | undefined): Promise<PrefsSnapshot> {
    if (!o || o.revision === 0n || !o.envelope) return { revision: 0n, prefs: emptyPrefs() };
    const { msg } = await openMessage(this.keys.prefs, o.envelope, { kind: PayloadKind.ACCOUNT_PREFS, requestId: PREFS_OBJECT, seq: o.revision }, ProjectPrefsSchema);
    if (msg.schema !== 1) throw new Error("项目偏好的格式比这个网页新，请刷新网页");
    return { revision: o.revision, prefs: msg };
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
      this.prefs = { ...this.prefs, status: this.prefs.snap ? "ready" : "error", error: errorText(e) };
    }
    this.changed();
  }

  /** Takes a copy from the server if it is newer than the one here. */
  private async takePrefs(o: AccountPrefsObject | undefined, pushed: boolean) {
    try {
      const snap = await this.openPrefs(o);
      if (this.prefs.snap && snap.revision <= this.prefs.snap.revision) return;
      // Changed by another client while nothing of this one's is in flight.
      const elsewhere = pushed && this.prefs.pending.size === 0 && !!this.prefs.snap;
      this.prefs = { ...this.prefs, snap, status: "ready", changedAt: elsewhere ? Date.now() : this.prefs.changedAt };
      this.changed();
    } catch (e) {
      this.report(e);
    }
  }

  /** Pins, unpins, removes or restores a project, for the whole account.
   * Only while connected: offline the list is shown as cached and changes
   * wait. Resolves false if it was not saved; the list then stays as it
   * was, and retryPrefs tries the same operation again. */
  private settingsWrites = new Map<string, Promise<boolean>>();

  changePrefs(kind: PrefKind, path: string, op: PrefOp = { id: newRequestID(), kind, path, at: Date.now() }): Promise<boolean> {
    if (kind !== "settings") return this.changePrefsNow(kind, path, op);
    const prior = this.settingsWrites.get(path);
    const next = (prior ? prior.catch(() => false).then(() => this.changePrefsNow(kind, path, op)) : this.changePrefsNow(kind, path, op));
    this.settingsWrites.set(path, next);
    void next.finally(() => { if (this.settingsWrites.get(path) === next) this.settingsWrites.delete(path); });
    return next;
  }

  private async changePrefsNow(kind: PrefKind, path: string, op: PrefOp): Promise<boolean> {
    if (this.conn !== "online" || this.prefs.status !== "ready") return false;
    this.prefs.pending.set(path, kind);
    this.prefs = { ...this.prefs, failed: undefined };
    this.changed();
    try {
      const snap = await commitOp(this.prefsStore, op, this.prefs.snap);
      if (!this.prefs.snap || snap.revision >= this.prefs.snap.revision) this.prefs = { ...this.prefs, snap };
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
  async uploadImage(machineId: string, p: { bytes: Uint8Array; format: string }, progress: (done: number, total: number) => void = () => undefined, started: (abort: () => void) => void = () => undefined): Promise<string> {
    const format = { jpeg: ImageFormat.JPEG, png: ImageFormat.PNG, webp: ImageFormat.WEBP, gif: ImageFormat.GIF }[p.format] ?? ImageFormat.UNSPECIFIED;
    const transferId = newRequestID();
    const sha256 = new Uint8Array(await crypto.subtle.digest("SHA-256", p.bytes as Uint8Array<ArrayBuffer>));
    const total = p.bytes.length;
    const statusOf = (r: CommandResult) => {
      const k = r.kind;
      const st = k.case === "beginImageUpload" || k.case === "putImageChunk" || k.case === "getImageUploadStatus" || k.case === "completeImageUpload" ? k.value.status : undefined;
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
            return statusOf(await this.machineCommand(machineId, { case: "getImageUploadStatus", value: { transferId } }));
          } catch (e2) {
            if (!uncertain(e2) || i >= 2) throw e2;
          }
        }
      }
    };
    let st = await step({ case: "beginImageUpload", value: { transferId, format, totalBytes: BigInt(total), sha256 } });
    // Dropped later (removed, 原图 switched, another machine): the machine
    // is told so rather than keeping it until it expires.
    started(() => void this.machineCommand(machineId, { case: "abortImageUpload", value: { transferId } }).catch(() => undefined));
    const chunk = Math.min(UPLOAD_CHUNK, st.maxChunkBytes > 0 ? st.maxChunkBytes : UPLOAD_CHUNK);
    for (let guard = 0; Number(st.persistedBytes) < total; guard++) {
      if (guard > total / chunk + 10) throw new Error("上传没有进展");
      const offset = Number(st.persistedBytes);
      progress(offset, total);
      st = await step({ case: "putImageChunk", value: { transferId, offset: BigInt(offset), data: p.bytes.subarray(offset, offset + chunk) } });
    }
    progress(total, total);
    st = await step({ case: "completeImageUpload", value: { transferId } });
    if (!st.imageId) throw new Error("电脑没有返回图片 ID");
    return st.imageId;
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
      if (e instanceof ConnectError && e.code === Code.NotFound) return { kind: "missing" };
      throw e;
    }
    if (res.expired || !res.image) return { kind: "expired" };
    let out: ImageResult;
    try {
      const { msg } = await openMessage(s.key, res.image, { kind: PayloadKind.IMAGE, machineId: s.machineId, sessionId: sid, requestId: imageId }, ImagePayloadSchema);
      out = matchesFormat(msg.data, msg.format) ? { kind: "ok", blob: new Blob([msg.data as Uint8Array<ArrayBuffer>], { type: mimeOf(msg.format) }), width: msg.width, height: msg.height, format: msg.format } : { kind: "bad" };
    } catch (e) {
      this.report(e);
      out = { kind: "bad" };
    }
    if (out.kind === "ok") {
      this.imageCache.set(key, out);
      while (this.imageCache.size > IMAGE_CACHE) this.imageCache.delete(this.imageCache.keys().next().value!);
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

  async setPushSubscription(subscriptionJson: string, kinds: NotifyKind[]): Promise<void> {
    await this.api.setPushSubscription({ subscriptionJson, kinds });
  }

  /** Unregisters a device; a machine's sessions go with it. */
  async removeDevice(deviceId: string): Promise<void> {
    await this.api.removeDevice({ deviceId });
    this.deviceRemovals.set(deviceId, (this.deviceRemovals.get(deviceId) ?? 0) + 1);
    this.devices.delete(deviceId);
    for (const [id, s] of this.sessions) if (s.machineId === deviceId) this.sessions.delete(id);
    this.machines.delete(deviceId);
    this.changed();
  }

  private models = new Map<string, Promise<ListModelsResponse>>();
  private modelReads = new Set<string>();
  private modelLists = new Map<string, ListModelsResponse>();

  /** A model's display name, once the machine's list has been fetched. */
  modelName(machineId: string, agent: Agent, slug: string): string {
    return modelLabel(this.modelLists.get(machineId + "/" + agent)?.models, slug, agent);
  }

  /** The models a machine's CLI offers; pickers refresh completed reads. */
  listModels(machineId: string, agent: Agent = Agent.CODEX, fresh = false): Promise<ListModelsResponse> {
    const key = machineId + "/" + agent;
    let p = fresh && !this.modelReads.has(key) ? undefined : this.models.get(key);
    if (!p) {
      p = this.machineCommand(machineId, { case: "listModels", value: { agent } }).then((r) => {
        if (r.kind.case !== "listModels") throw new Error("意外的回复");
        this.modelLists.set(key, r.kind.value);
        this.changed();
        return r.kind.value;
      });
      this.modelReads.add(key);
      p.then(() => this.modelReads.delete(key), () => {
        this.modelReads.delete(key);
        if (this.models.get(key) === p) this.models.delete(key);
      });
      this.models.set(key, p);
    }
    return p;
  }

  imageStorage = new Map<string, ImageStorageUsage>();

  private settingsCache = new Map<string, Promise<DaemonSettings | undefined>>();

  /** A machine's daemon settings, fetched once per page load. */
  settings(machineId: string, fresh = false): Promise<DaemonSettings | undefined> {
    let p = fresh ? undefined : this.settingsCache.get(machineId);
    if (!p) {
      p = this.machineCommand(machineId, { case: "getSettings", value: {} }).then((r) => {
        if (r.kind.case !== "getSettings") throw new Error("意外的回复");
        if (r.kind.value.imageStorage) this.imageStorage.set(machineId, r.kind.value.imageStorage);
        this.changed();
        return r.kind.value.settings;
      });
      p.catch(() => this.settingsCache.delete(machineId));
      this.settingsCache.set(machineId, p);
    }
    return p;
  }

  async saveSettings(machineId: string, next: DaemonSettings, cleanImages = false): Promise<DaemonSettings | undefined> {
    const r = await this.machineCommand(machineId, { case: "updateSettings", value: { settings: next, cleanImages } });
    if (r.kind.case !== "updateSettings") throw new Error("意外的回复");
    if (r.kind.value.imageStorage) this.imageStorage.set(machineId, r.kind.value.imageStorage);
    this.changed();
    this.settingsCache.set(machineId, Promise.resolve(r.kind.value.settings));
    return r.kind.value.settings;
  }

  private report(e: unknown) {
    console.warn("remote-agent:", errorText(e));
    this.error = errorText(e);
    this.changed();
  }
}

/** Request IDs of the messages recorded among events. */
function recordedIds(events: Event[]): string[] {
  return events.flatMap((e) => (e.kind.case === "userMessage" && e.kind.value.requestId ? [e.kind.value.requestId] : []));
}

function maxBig(a: bigint, ...rest: bigint[]): bigint {
  return rest.reduce((m, x) => (x > m ? x : m), a);
}

export function errorText(e: unknown): string {
  if (e instanceof CommandError) return e.message;
  if (e instanceof ConnectError) return e.rawMessage || e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}
