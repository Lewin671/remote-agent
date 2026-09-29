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
import { PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import type { Event } from "./gen/remoteagent/v1/event_pb";
import { SessionSchema, type ListModelsResponse, type Session } from "./gen/remoteagent/v1/local_pb";
import {
  AuthService,
  ClientService,
  DeviceKind,
  DeviceRegistrationSchema,
  PeerFrameSchema,
  ServerFrameSchema,
  type Machine as RemoteMachine,
  type RemoteSession,
  type StoredEvent,
} from "./gen/remoteagent/v1/relay_pb";
import { CommandResultSchema, CommandSchema, MachineMetaSchema, type CommandResult, type MachineMeta } from "./gen/remoteagent/v1/remote_pb";

export type CommandKind = MessageInitShape<typeof CommandSchema>["kind"];
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
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
}

export class CommandError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

/** Where the API is reached: the account's server, or with
 * VITE_SAME_ORIGIN_API the page's own origin, which forwards to it. */
export function apiBase(server: string): string {
  return import.meta.env.VITE_SAME_ORIGIN_API ? location.origin : server;
}

export type ConnState = "connecting" | "online" | "offline";

const COMMAND_TTL_MS = 60_000;

/** Joins the account in secretText with a new browser device. */
export async function join(secretText: string): Promise<StoredAccount> {
  const secret = parseSecret(secretText);
  const root = await importRoot(secret.root);
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
  const auth = createClient(AuthService, createConnectTransport({ baseUrl: apiBase(secret.server), useBinaryFormat: true }));
  const res = await auth.registerDevice({ registration: reg, signature: acct.sign(concat(new TextEncoder().encode(REGISTER_CONTEXT), reg)) });
  if (res.deviceId !== (await deviceID(pub))) throw new Error("server 返回了意外的设备 ID");
  const stored: StoredAccount = { server: secret.server, root, devicePrivate: device.privateKey, deviceId: res.deviceId };
  await saveAccount(stored);
  return stored;
}

export class Remote {
  machines = new Map<string, MachineView>();
  sessions = new Map<string, SessionView>();
  conn: ConnState = "connecting";
  error = "";

  private buffers = new Map<string, EventBuffer>();
  // Pushes seen per machine, so that a list fetched before a push cannot
  // undo the online state it reported.
  private machinePushes = new Map<string, number>();
  private token = "";
  private api: Client<typeof ClientService>;
  private ws?: WebSocket;
  private retry = 1000;
  private closed = false;
  private listeners = new Set<() => void>();
  private version = 0;

  private constructor(
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

  private changed() {
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
        this.error = "";
        this.changed();
        // Subscribed first, then catch up: nothing falls in between.
        await this.refresh();
        for (const sid of this.buffers.keys()) void this.fetchEvents(sid);
        break;
      case "machineUpdated":
        if (kind.value.machine) {
          const id = kind.value.machine.id;
          this.machinePushes.set(id, (this.machinePushes.get(id) ?? 0) + 1);
          await this.putMachine(kind.value.machine);
        }
        break;
      case "sessionUpdated":
        if (kind.value.session) await this.putSession(kind.value.session);
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

  /** Reloads machines and sessions. */
  async refresh() {
    try {
      const pushes = new Map(this.machinePushes);
      const [ms, ss] = await Promise.all([this.api.listMachines({}), this.api.listRemoteSessions({})]);
      for (const m of ms.machines) {
        const stale = (this.machinePushes.get(m.id) ?? 0) !== (pushes.get(m.id) ?? 0);
        await this.putMachine(m, stale);
      }
      for (const s of ss.sessions) await this.putSession(s);
    } catch (e) {
      this.report(e);
    }
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
    try {
      if (!view) {
        const { header, plaintext } = await open(this.keys.wrap, rs.key);
        expect(header, { kind: PayloadKind.SESSION_KEY, machineId: rs.machineId, sessionId: rs.sessionId });
        view = {
          id: rs.sessionId,
          machineId: rs.machineId,
          key: await importSessionKey(plaintext as Uint8Array<ArrayBuffer>),
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
      this.buffers.set(sid, { events: [], pending: new Map(), fetching: false, refetch: false, loaded: false });
      void this.fetchEvents(sid);
      return [];
    }
    return b.events;
  }

  loaded(sid: string): boolean {
    return this.buffers.get(sid)?.loaded ?? false;
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
    } catch (e) {
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

  /** Sends a command about a session. */
  async command(sid: string, cmd: CommandKind): Promise<CommandResult> {
    const s = this.sessions.get(sid);
    if (!s) throw new Error("未知会话");
    return this.forward(s.machineId, sid, cmd);
  }

  /** Sends a command about a machine, such as starting a session. */
  machineCommand(machineId: string, cmd: CommandKind): Promise<CommandResult> {
    return this.forward(machineId, "", cmd);
  }

  /** Seals cmd, forwards it and returns the opened result. Network
   * failures are retried with the same request ID, which the daemon
   * runs at most once. */
  private async forward(machineId: string, sid: string, cmd: CommandKind): Promise<CommandResult> {
    const requestId = newRequestID();
    const deadline = Date.now() + COMMAND_TTL_MS;
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
        throw e;
      }
    }
  }

  private models = new Map<string, Promise<ListModelsResponse>>();

  /** The models a machine's Codex offers, fetched once per page load. */
  listModels(machineId: string): Promise<ListModelsResponse> {
    let p = this.models.get(machineId);
    if (!p) {
      p = this.machineCommand(machineId, { case: "listModels", value: {} }).then((r) => {
        if (r.kind.case !== "listModels") throw new Error("意外的回复");
        return r.kind.value;
      });
      p.catch(() => this.models.delete(machineId));
      this.models.set(machineId, p);
    }
    return p;
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
