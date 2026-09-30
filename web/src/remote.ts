// Remote keeps the page in sync with the server: it logs in, holds the
// decrypted machines, sessions and events, follows the push stream, and
// sends sealed commands. Content is only ever decrypted here.

import { create, fromBinary, toBinary, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient, type Client, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  accountKey,
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
  signLogin,
  expect,
  type Keys,
} from "./crypto";
import { EnvelopeSchema, PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import { Agent, type Event } from "./gen/remoteagent/v1/event_pb";
import { SessionSchema, type DaemonSettings, type ListModelsResponse, type Session } from "./gen/remoteagent/v1/local_pb";
import {
  AuthService,
  ClientService,
  DeviceKind,
  DeviceRegistrationSchema,
  NotifyKind,
  PeerFrameSchema,
  ServerFrameSchema,
  TermHelloSchema,
  type Machine as RemoteMachine,
  type RemoteSession,
  type StoredEvent,
} from "./gen/remoteagent/v1/relay_pb";
import { CommandResultSchema, CommandSchema, MachineMetaSchema, TerminalFrameSchema, type CommandResult, type MachineMeta, type TerminalFrame } from "./gen/remoteagent/v1/remote_pb";

export type CommandKind = MessageInitShape<typeof CommandSchema>["kind"];
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
import { findModel, type Outgoing } from "./model";
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
  // The last fetch failed; a retry may help.
  error: string;
  // An event did not open; history stops before it.
  broken: boolean;
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
  return import.meta.env.VITE_SAME_ORIGIN_API ? location.origin : server;
}

export interface TermConn {
  send(frame: MessageInitShape<typeof TerminalFrameSchema>): void;
  close(): void;
}

export type ConnState = "connecting" | "online" | "offline";

export const COMMAND_TTL_MS = 60_000;

/** Joins the account in secretText with a new browser device. */
export async function join(secretText: string): Promise<StoredAccount> {
  const secret = parseSecret(secretText);
  return joinWithRoot(secret.root, secret.server);
}

/** Registers this browser as a device of the account with root key root. */
export async function joinWithRoot(rootRaw: Uint8Array<ArrayBuffer>, server: string): Promise<StoredAccount> {
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
  const auth = createClient(AuthService, createConnectTransport({ baseUrl: apiBase(server), useBinaryFormat: true }));
  const res = await auth.registerDevice({ registration: reg, signature: acct.sign(concat(new TextEncoder().encode(REGISTER_CONTEXT), reg)) });
  if (res.deviceId !== (await deviceID(pub))) throw new Error("server 返回了意外的设备 ID");
  const stored: StoredAccount = { server, root, rootRaw: new Uint8Array(rootRaw), devicePrivate: device.privateKey, deviceId: res.deviceId };
  await saveAccount(stored);
  localStorage.setItem("ra-server", server);
  return stored;
}

export class Remote {
  machines = new Map<string, MachineView>();
  sessions = new Map<string, SessionView>();
  conn: ConnState = "connecting";
  error = "";
  // Whether the stream has been up at least once, and how many attempts
  // failed since it last was: tells "connecting" from "reconnecting" and
  // from "cannot reach the server".
  everOnline = false;
  failures = 0;
  connError = "";
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
  // Pushes seen per machine, session and machine removal, so that a list
  // fetched before a push cannot undo it.
  private machinePushes = new Map<string, number>();
  private sessionPushes = new Map<string, number>();
  private removedPushes = new Map<string, number>();
  private token = "";
  private api: Client<typeof ClientService>;
  private ws?: WebSocket;
  private retry = 1000;
  private closed = false;
  private listeners = new Set<() => void>();
  private version = 0;

  protected constructor(
    readonly account: StoredAccount,
    private keys: Keys,
  ) {
    const auth: Interceptor = (next) => (req) => {
      req.header.set("Authorization", "Bearer " + this.token);
      return next(req);
    };
    this.api = createClient(ClientService, createConnectTransport({ baseUrl: apiBase(account.server), useBinaryFormat: true, interceptors: [auth] }));
  }

  static async load(): Promise<Remote | undefined> {
    const acct = await loadAccount();
    if (!acct) return undefined;
    return new Remote(acct, await deriveKeys(acct.root));
  }

  // --- change notification for React ---

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  snapshot = () => this.version;

  protected changed() {
    this.version++;
    for (const fn of this.listeners) fn();
  }

  // --- connection ---

  start() {
    this.closed = false;
    void this.connect();
  }

  stop() {
    this.closed = true;
    this.ws?.close();
  }

  private async login() {
    const auth = createClient(AuthService, createConnectTransport({ baseUrl: apiBase(this.account.server), useBinaryFormat: true }));
    const ch = await auth.getChallenge({ deviceId: this.account.deviceId });
    const sig = await signLogin(this.account.devicePrivate, ch.challenge);
    const res = await auth.login({ deviceId: this.account.deviceId, challenge: ch.challenge, signature: sig });
    this.token = res.token;
  }

  private async connect() {
    if (this.closed) return;
    this.conn = "connecting";
    this.changed();
    try {
      await this.login();
    } catch (e) {
      this.fail(e);
      return;
    }
    const url = apiBase(this.account.server).replace(/^http/, "ws") + "/v1/stream";
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    ws.onopen = () => {
      const hello = create(PeerFrameSchema, { kind: { case: "hello", value: { token: this.token } } });
      ws.send(toBinary(PeerFrameSchema, hello));
    };
    ws.onmessage = (m) => {
      let f;
      try {
        f = fromBinary(ServerFrameSchema, new Uint8Array(m.data as ArrayBuffer));
      } catch {
        ws.close();
        return;
      }
      void this.onFrame(f.kind);
    };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = undefined;
      this.fail(new Error("与 server 的连接断开"));
    };
  }

  private fail(e: unknown) {
    this.conn = "offline";
    this.failures++;
    this.connError = errorText(e);
    this.error = errorText(e);
    this.changed();
    if (this.closed) return;
    const delay = this.retry;
    this.retry = Math.min(this.retry * 2, 30_000);
    setTimeout(() => void this.connect(), delay + Math.random() * delay * 0.25);
  }

  private async onFrame(kind: ReturnType<typeof fromBinary<typeof ServerFrameSchema>>["kind"]) {
    switch (kind.case) {
      case "ready":
        this.retry = 1000;
        this.conn = "online";
        this.everOnline = true;
        this.failures = 0;
        this.connError = "";
        this.error = "";
        this.changed();
        // Subscribed first, then catch up: nothing falls in between.
        await this.refresh();
        for (const sid of this.buffers.keys()) void this.fetchEvents(sid);
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
      for (const m of ms.machines) {
        if (removed(m.id)) continue;
        await this.putMachine(m, pushed(this.machinePushes, mp, m.id));
      }
      for (const s of ss.sessions) if (!removed(s.machineId)) await this.putSession(s);
      this.listed = true;
      this.listError = "";
      this.listRetry = 2000;
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
    if (!b) {
      this.buffers.set(sid, { events: [], pending: new Map(), fetching: false, refetch: false, loaded: false, error: "", broken: false });
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

  /** Fetches everything after the last contiguous event. */
  private async fetchEvents(sid: string) {
    const b = this.buffers.get(sid);
    const s = this.sessions.get(sid);
    if (!b || !s) return;
    if (b.fetching) {
      b.refetch = true;
      return;
    }
    b.fetching = true;
    try {
      do {
        b.refetch = false;
        for (;;) {
          const before = b.events.length;
          const res = await this.api.getEvents({ sessionId: sid, afterSeq: BigInt(before), limit: 500 });
          await this.ingest(s, res.events, false);
          // An event that does not open stops progress; asking for the
          // same page again would not help.
          if (!res.more || b.events.length === before) break;
        }
      } while (b.refetch);
      b.loaded = true;
      b.error = "";
    } catch (e) {
      b.error = errorText(e);
      this.report(e);
    } finally {
      b.fetching = false;
      this.changed();
    }
  }

  private async ingest(s: SessionView, stored: StoredEvent[], fillGaps = true) {
    const b = this.buffers.get(s.id);
    if (!b) return;
    for (const se of stored) {
      if (se.seq <= BigInt(b.events.length) || b.pending.has(se.seq)) continue;
      try {
        const { msg } = await openMessage(
          s.key,
          se.envelope,
          { kind: PayloadKind.EVENT, machineId: s.machineId, sessionId: s.id, seq: se.seq },
          EventSchema,
        );
        if (msg.seq !== se.seq || msg.sessionId !== s.id) throw new Error("事件的位置被改动");
        b.pending.set(se.seq, msg);
      } catch (e) {
        b.broken = true;
        this.report(e);
        return;
      }
    }
    for (let next = BigInt(b.events.length + 1); b.pending.has(next); next++) {
      b.events.push(b.pending.get(next)!);
      b.pending.delete(next);
    }
    s.lastSeq = maxBig(s.lastSeq, BigInt(b.events.length));
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
    const env = await sealMessage(
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
      create(CommandSchema, { kind: cmd }),
    );
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
        const unknown = [Code.DeadlineExceeded, Code.Unknown, Code.Internal, Code.Canceled].includes(ce.code);
        if (unknown && attempt < 3 && Date.now() + 2000 < deadline) {
          await new Promise((r) => setTimeout(r, 1000 * (attempt + 1)));
          continue;
        }
        if (ce.code === Code.Unavailable) throw new CommandError("unavailable", "电脑不在线，指令没有发出");
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
  async sendMessage(sid: string, text: string, sendId: string, slot: RequestSlot): Promise<RequestSlot> {
    const cmd: CommandKind = { case: "send", value: { sessionId: sid, text, requestId: sendId } };
    try {
      await this.command(sid, cmd, slot);
      return slot;
    } catch (e) {
      if (!(e instanceof CommandError) || e.code !== "deadline_exceeded" || Date.now() < slot.deadline) throw e;
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

  /** Sends text to a session; its progress shows in outbox(sid). */
  send(sid: string, text: string) {
    const o: Outgoing = { sendId: newRequestID(), ...newSlot(), text, status: "sending" };
    this.outboxes.set(sid, [...this.outbox(sid), o]);
    this.changed();
    void this.deliver(sid, o);
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
      const slot = await this.sendMessage(sid, o.text, o.sendId, { requestId: o.requestId, deadline: o.deadline });
      this.patchOut(sid, o.sendId, { status: "sent", ...slot });
    } catch (e) {
      // The send ID still keeps the daemon from queueing the text twice.
      this.patchOut(sid, o.sendId, { status: "failed", error: errorText(e), ...(uncertain(e) ? {} : newSlot()) });
    }
  }

  /** Joins a session's terminal stream. Frames are sealed with the session
   * key and numbered; each side refuses frames out of order. */
  async openTerminal(sid: string, onFrame: (f: TerminalFrame) => void, onClose: (reason: string) => void): Promise<TermConn> {
    const s = this.sessions.get(sid);
    if (!s) throw new Error("未知会话");
    const streamId = newRequestID();
    const ws = new WebSocket(apiBase(this.account.server).replace(/^http/, "ws") + "/v1/term");
    ws.binaryType = "arraybuffer";
    let closed = false;
    const finish = (reason: string) => {
      if (closed) return;
      closed = true;
      onClose(reason);
    };
    await new Promise<void>((resolve, reject) => {
      ws.onopen = () => {
        ws.send(toBinary(TermHelloSchema, create(TermHelloSchema, { token: this.token, streamId, machineId: s.machineId })));
        resolve();
      };
      ws.onerror = () => reject(new Error("连不上终端"));
    });
    let last = 0n;
    let queue = Promise.resolve();
    ws.onmessage = (m) => {
      // Open in arrival order.
      queue = queue.then(async () => {
        try {
          const env = fromBinary(EnvelopeSchema, new Uint8Array(m.data as ArrayBuffer));
          const { header, msg } = await openMessage(
            s.key,
            env,
            { kind: PayloadKind.TERMINAL_OUTPUT, machineId: s.machineId, sessionId: sid, requestId: streamId },
            TerminalFrameSchema,
          );
          if (header.seq <= last) throw new Error("终端帧顺序不对");
          last = header.seq;
          onFrame(msg);
        } catch (e) {
          finish(errorText(e));
          ws.close();
        }
      });
    };
    ws.onclose = () => finish("终端连接断开");
    try {
      await this.command(sid, { case: "openTerminal", value: { sessionId: sid, streamId } });
    } catch (e) {
      ws.close();
      throw e;
    }
    let seq = 0n;
    let sending = Promise.resolve();
    return {
      send: (frame) => {
        sending = sending.then(async () => {
          if (ws.readyState !== WebSocket.OPEN) return;
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
        ws.close();
      },
    };
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
    for (const [id, s] of this.sessions) if (s.machineId === deviceId) this.sessions.delete(id);
    this.machines.delete(deviceId);
    this.changed();
  }

  private models = new Map<string, Promise<ListModelsResponse>>();
  private modelLists = new Map<string, ListModelsResponse>();

  /** A model's display name, once the machine's list has been fetched. */
  modelName(machineId: string, agent: Agent, slug: string): string {
    return findModel(this.modelLists.get(machineId + "/" + agent)?.models, slug)?.displayName || slug;
  }

  /** The models a machine's CLI offers, fetched once per page load. */
  listModels(machineId: string, agent: Agent = Agent.CODEX): Promise<ListModelsResponse> {
    const key = machineId + "/" + agent;
    let p = this.models.get(key);
    if (!p) {
      p = this.machineCommand(machineId, { case: "listModels", value: { agent } }).then((r) => {
        if (r.kind.case !== "listModels") throw new Error("意外的回复");
        this.modelLists.set(key, r.kind.value);
        this.changed();
        return r.kind.value;
      });
      p.catch(() => this.models.delete(key));
      this.models.set(key, p);
    }
    return p;
  }

  private settingsCache = new Map<string, Promise<DaemonSettings | undefined>>();

  /** A machine's daemon settings, fetched once per page load. */
  settings(machineId: string, fresh = false): Promise<DaemonSettings | undefined> {
    let p = fresh ? undefined : this.settingsCache.get(machineId);
    if (!p) {
      p = this.machineCommand(machineId, { case: "getSettings", value: {} }).then((r) => {
        if (r.kind.case !== "getSettings") throw new Error("意外的回复");
        return r.kind.value.settings;
      });
      p.catch(() => this.settingsCache.delete(machineId));
      this.settingsCache.set(machineId, p);
    }
    return p;
  }

  async saveSettings(machineId: string, next: DaemonSettings): Promise<DaemonSettings | undefined> {
    const r = await this.machineCommand(machineId, { case: "updateSettings", value: { settings: next } });
    if (r.kind.case !== "updateSettings") throw new Error("意外的回复");
    this.settingsCache.set(machineId, Promise.resolve(r.kind.value.settings));
    return r.kind.value.settings;
  }

  private report(e: unknown) {
    console.warn("remote-agent:", errorText(e));
    this.error = errorText(e);
    this.changed();
  }
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
