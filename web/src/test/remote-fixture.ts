import {
  create,
  fromBinary,
  toBinary,
  type DescMessage,
  type MessageShape,
} from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { parseTestHeader, testRequestIdentity, unpackTestBody } from "./stored-security-fixture";
import { SecurityOpenEnvelopeOpSchema } from "../gen/remoteagent/core/v1/security_pb";
import { EnvelopeSchema, type Envelope, type Header } from "../gen/remoteagent/v1/envelope_pb";
import { getCoreHost } from "../core/host";
import { Remote } from "../remote";
import { Purpose } from "./stored-security-fixture";
import { GoSecurityFixture, webMember } from "./security-fixture";
import { CommandResultSchema, MachineMetaSchema, type LiveSnapshot, type MachineMeta } from "../gen/remoteagent/v1/remote_pb";
import { SessionSchema, type Session } from "../gen/remoteagent/v1/local_pb";
import { SessionStatus } from "../gen/remoteagent/v1/event_pb";
import {
  ForwardRequestSchema,
  ForwardResponseSchema,
  GetAccountPrefsRequestSchema,
  GetAccountPrefsResponseSchema,
  GetChallengeResponseSchema,
  GetEventsRequestSchema,
  GetEventsResponseSchema,
  GetImageRequestSchema,
  GetImageResponseSchema,
  GetPushKeyResponseSchema,
  GetSecurityStatesRequestSchema,
  GetSecurityStatesResponseSchema,
  ListDevicesResponseSchema,
  ListMachinesResponseSchema,
  ListRemoteSessionsRequestSchema,
  ListRemoteSessionsResponseSchema,
  LoginResponseSchema,
  PutAccountPrefsRequestSchema,
  PutAccountPrefsResponseSchema,
  PutDeviceMetaRequestSchema,
  PutDeviceMetaResponseSchema,
  SetPushSubscriptionRequestSchema,
  SetPushSubscriptionResponseSchema,
  SetPushTargetRequestSchema,
  SetPushTargetResponseSchema,
} from "../gen/remoteagent/v1/relay_pb";
import {
  EventSchema as CoreEventSchema,
  StreamClosedEventSchema,
  StreamReadyEventSchema,
  SyncLifecycleActionSchema,
  SyncLifecycleKind,
  TimerKind,
} from "../gen/remoteagent/core/v1/app_pb";
import { ConnState as CoreConnState, HomeEmptyKind } from "../gen/remoteagent/core/v1/home_view_pb";
import type { DeviceInfo } from "../devices";

export type TestConnState = "connecting" | "online" | "offline";
export type TestMachineView = { id: string; online: boolean; lastSeenMs: bigint; meta?: MachineMeta; metaRev: bigint };
export type TestSessionView = {
  id: string;
  machineId: string;
  historyError?: string;
  info?: Session;
  checkpoint?: LiveSnapshot;
  checkpointStatus?: "ready" | "waiting" | "invalid";
  metaRev: bigint;
  lastSeq: bigint;
  updatedMs: bigint;
  endedMs: bigint;
};
type ConnState = TestConnState;
type MachineView = TestMachineView;
type SessionView = TestSessionView;

export function demoMachines(r: Remote): ReadonlyMap<string, TestMachineView> {
  const host = r as unknown as { demoMachines?: Map<string, TestMachineView>; testMachines?: Map<string, TestMachineView> };
  return host.demoMachines ?? host.testMachines ?? new Map();
}

export function demoSessions(r: Remote): ReadonlyMap<string, TestSessionView> {
  const host = r as unknown as { demoSessions?: Map<string, TestSessionView>; testSessions?: Map<string, TestSessionView> };
  return host.demoSessions ?? host.testSessions ?? new Map();
}

export function demoDevices(r: Remote): ReadonlyMap<string, DeviceInfo> {
  const host = r as unknown as { demoDevices?: Map<string, DeviceInfo> };
  return host.demoDevices ?? new Map();
}

export function remoteConnState(r: Remote): TestConnState {
  if (!globalThis.__remoteAgentCore) return "connecting";
  const c = getCoreHost(r).viewConnection();
  return c.removed ? "offline" : c.connected ? "online" : c.state === CoreConnState.OFFLINE ? "offline" : "connecting";
}

export function isRemoteListed(r: Remote): boolean {
  if (!globalThis.__remoteAgentCore) return false;
  return getCoreHost(r).viewHome(1).empty?.kind !== HomeEmptyKind.LOADING;
}

export function isPrefsReady(r: Remote): boolean {
  if (!globalThis.__remoteAgentCore) return true;
  return !getCoreHost(r).viewHome(1).prefsBanner?.errorTone;
}

export function isPrefsSyncing(r: Remote): boolean {
  if (!globalThis.__remoteAgentCore) return false;
  return !!getCoreHost(r).viewHome(1).prefsBanner?.syncing;
}

export interface SyncStatePatch {
  conn?: ConnState;
  moreSessions?: boolean;
  connError?: string;
  machines?: Map<string, MachineView>;
  sessions?: Map<string, SessionView>;
  devices?: Map<string, DeviceInfo>;
}

export function patchSyncState(r: Remote, patch: SyncStatePatch): void {
  const demo = r as unknown as {
    demoMachines?: Map<string, MachineView>;
    demoSessions?: Map<string, SessionView>;
    demoDevices?: Map<string, DeviceInfo>;
    demoHasMoreSessions?: boolean;
    demoStream?: { legId: bigint; ready: boolean };
    opts?: URLSearchParams;
    setDemoConnection?(conn: ConnState): void;
    syncDemoToCore?(forceRefresh?: boolean): void;
  };
  if (demo.syncDemoToCore) {
    if (patch.moreSessions !== undefined && "demoHasMoreSessions" in demo) {
      demo.demoHasMoreSessions = patch.moreSessions;
    }
    if (patch.machines && demo.demoMachines) {
      demo.demoMachines.clear();
      for (const [k, v] of patch.machines) demo.demoMachines.set(k, v);
    }
    if (patch.sessions && demo.demoSessions) {
      demo.demoSessions.clear();
      for (const [k, v] of patch.sessions) demo.demoSessions.set(k, v);
    }
    if (patch.devices && demo.demoDevices) {
      demo.demoDevices.clear();
      for (const [k, v] of patch.devices) demo.demoDevices.set(k, v);
    }
    if (patch.conn && demo.setDemoConnection) {
      demo.setDemoConnection(patch.conn);
    }
    if (
      (patch.machines || patch.sessions || patch.devices || patch.moreSessions !== undefined) &&
      demo.demoStream
    ) {
      demo.syncDemoToCore(patch.moreSessions !== undefined);
    }
    return;
  }

  // Non-Demo Remote (e.g. SyntheticRemote / plain Remote in unit tests): drive Core directly.
  const synth = r as unknown as { testSessions?: Map<string, SessionView>; testMachines?: Map<string, MachineView> };
  if (patch.sessions && synth.testSessions) {
    synth.testSessions.clear();
    for (const [k, v] of patch.sessions) synth.testSessions.set(k, v);
  }
  if (patch.machines && synth.testMachines) {
    synth.testMachines.clear();
    for (const [k, v] of patch.machines) synth.testMachines.set(k, v);
  }
  if (!globalThis.__remoteAgentCore) return;
  const host = getCoreHost(r);
  if (patch.conn === "online") {
    host.dispatch(create(CoreEventSchema, { event: { case: "syncLifecycle", value: create(SyncLifecycleActionSchema, { kind: SyncLifecycleKind.START }) } }));
    host.dispatch(create(CoreEventSchema, { event: { case: "streamReady", value: create(StreamReadyEventSchema, { legId: 1n, serverTimeMs: BigInt(Date.now()) }) } }));
  } else if (patch.conn === "offline") {
    host.dispatch(create(CoreEventSchema, { event: { case: "streamClosed", value: create(StreamClosedEventSchema, { legId: 1n, errorCode: "unavailable", error: patch.connError ?? "offline" }) } }));
    host.fireActiveTimer(TimerKind.LINK_GRACE, "display");
    host.fireActiveTimer(TimerKind.LINK_GRACE);
  }
}

const CONNECT_CODE_NAMES: Partial<Record<Code, string>> = {
  [Code.Canceled]: "canceled",
  [Code.Unknown]: "unknown",
  [Code.InvalidArgument]: "invalid_argument",
  [Code.DeadlineExceeded]: "deadline_exceeded",
  [Code.NotFound]: "not_found",
  [Code.AlreadyExists]: "already_exists",
  [Code.PermissionDenied]: "permission_denied",
  [Code.ResourceExhausted]: "resource_exhausted",
  [Code.FailedPrecondition]: "failed_precondition",
  [Code.Aborted]: "aborted",
  [Code.OutOfRange]: "out_of_range",
  [Code.Unimplemented]: "unimplemented",
  [Code.Internal]: "internal",
  [Code.Unavailable]: "unavailable",
  [Code.DataLoss]: "data_loss",
  [Code.Unauthenticated]: "unauthenticated",
};

type B = Uint8Array<ArrayBuffer>;
type Fields = Omit<Partial<Header>, "$typeName">;

export class SyntheticRemote extends Remote {
  mockRelayEpoch = "fixture-epoch";
  readonly testSessions = new Map<string, SessionView>();
  readonly testMachines = new Map<string, MachineView>();
  protected override sessionMachineId(sid: string): string {
    return super.sessionMachineId(sid) || this.testSessions.get(sid)?.machineId || "";
  }
  get sessions(): ReadonlyMap<string, SessionView> { return this.testSessions; }
  get machines(): ReadonlyMap<string, MachineView> { return this.testMachines; }
  private custom: Record<string, unknown> = {};
  private readonly defaults: Record<string, unknown>;
  readonly seed: B;

  constructor(
    readonly world: Awaited<ReturnType<typeof GoSecurityFixture.create>>,
    readonly member: Awaited<ReturnType<typeof webMember>>,
    seed: B,
  ) {
    const rpcHolder: { handle?: (url: string, body: Uint8Array, signal?: AbortSignal) => Promise<Response> } = {};
    const fetchFn: typeof fetch = async (input, init) => {
      const url = typeof input === "string" ? input : input instanceof Request ? input.url : String(input);
      let body: Uint8Array = new Uint8Array();
      if (init?.body instanceof Uint8Array) {
        body = init.body;
      } else if (init?.body instanceof ArrayBuffer) {
        body = new Uint8Array(init.body);
      } else if (typeof init?.body === "string") {
        body = new TextEncoder().encode(init.body);
      }
      return rpcHolder.handle!(url, body, init?.signal ?? undefined);
    };
    super(member.account, undefined, undefined, undefined, undefined, member.store, fetchFn);
    this.seed = seed;
    rpcHolder.handle = (url, body, signal) => this.handleMockRpc(url, body, signal);
    this.defaults = {
      getEvents: async () => ({ events: [], more: false }),
      listDevices: async () => ({ devices: [], currentDeviceId: member.account.deviceId }),
      listMachines: async () => ({
        machines: [{ id: this.machine, online: true, lastSeenMs: 0n }],
      }),
      listRemoteSessions: async () => ({ sessions: [] }),
      putDeviceMeta: async () => ({ applied: true }),
      putAccountPrefs: async () => ({ applied: true }),
      getAccountPrefs: async () => ({}),
      getPushKey: async () => ({ publicKey: "AAAA", machineDiskNotifications: false }),
      setPushSubscription: async () => ({}),
      setPushTarget: async () => ({ enabled: true }),
      getChallenge: async () => ({ challenge: crypto.getRandomValues(new Uint8Array(32)) }),
      login: async () => ({
        token: "synthetic-token",
        expiresMs: BigInt(Date.now() + 7 * 86_400_000),
        devicePublicKey: new Uint8Array(),
      }),
      getSecurityStates: async (req: { afterEpoch: bigint; limit: number }) =>
        world.fixture.statesResponse(member.account.deviceId, req.afterEpoch, req.limit),
    };
    Object.defineProperty(this, "api", {
      configurable: true,
      get: () => ({
        ...this.defaults,
        ...this.custom,
        forward: (req: unknown, opts?: unknown) => this.invokeMockForward(req, opts),
      }),
      set: (api: Record<string, unknown>) => {
        this.custom = api;
      },
    });
  }

  private async invokeMockForward(req: unknown, opts?: unknown): Promise<Record<string, unknown>> {
    const forward = this.custom.forward as ((req: unknown, opts?: unknown) => Promise<unknown>) | undefined;
    if (!forward) {
      throw new ConnectError("synthetic endpoint offline", Code.Unavailable);
    }
    const request = req as { command?: Envelope; attemptId: string };
    const res = (await forward(req, opts)) as {
      result?: Envelope;
      outcome?: string;
      reason?: string;
      attemptId?: string;
    };
    try {
      if (res.result && request.command) {
        const original = await this.world.fixture.open(this.machine, request.command);
        const opened = await this.world.fixture.open(this.member.account.deviceId, res.result);
        const proof = fromBinary(
          CommandResultSchema,
          unpackTestBody(parseTestHeader(new Uint8Array(res.result.securityHeader)), opened.plaintext),
        );
        if (!proof.outcome) {
          const binding = await testRequestIdentity(
            parseTestHeader(new Uint8Array(request.command.securityHeader)),
            unpackTestBody(parseTestHeader(new Uint8Array(request.command.securityHeader)), original.plaintext),
          );
          proof.outcome = "executed";
          proof.reason = "completed";
          proof.resultAvailable = true;
          proof.requestKeyHash = binding.key;
          proof.payloadHash = binding.payload;
          res.result = await this.seal({ ...opened.header, compression: 0 }, CommandResultSchema, proof);
        }
        res.outcome ??= proof.outcome;
        res.reason ??= proof.reason;
        res.attemptId ??= request.attemptId;
      }
    } catch {
      /* Keep deliberate signature/ciphertext corruption. */
    }
    return res as Record<string, unknown>;
  }

  private async handleMockRpc(url: string, body: Uint8Array, signal?: AbortSignal): Promise<Response> {
    const path = new URL(url, "https://ra.example.com").pathname;
    const protoOk = <D extends DescMessage>(schema: D, msg: MessageShape<D>) =>
      new Response(new Uint8Array(toBinary(schema, msg)), {
        status: 200,
        headers: {
          "Content-Type": "application/proto",
          ...(this.mockRelayEpoch ? { "Remote-Agent-Relay-Epoch": this.mockRelayEpoch } : {}),
        },
      });
    try {
      switch (path) {
        case "/healthz": {
          const fn = this.custom.healthz as ((url: string, opts?: { signal?: AbortSignal }) => Promise<Response>) | undefined;
          if (fn) return await fn(url, { signal });
          return new Response("ok", {
            status: 200,
            headers: this.mockRelayEpoch ? { "Remote-Agent-Relay-Epoch": this.mockRelayEpoch } : {},
          });
        }
        case "/remoteagent.v1.ClientService/GetSecurityStates": {
          const req = fromBinary(GetSecurityStatesRequestSchema, body);
          const fn = (this.custom.getSecurityStates ?? this.defaults.getSecurityStates) as (
            q: typeof req,
          ) => Promise<Record<string, unknown>>;
          const res = await fn(req);
          return protoOk(
            GetSecurityStatesResponseSchema,
            create(GetSecurityStatesResponseSchema, res as never),
          );
        }
        case "/remoteagent.v1.ClientService/ListDevices": {
          const fn = (this.custom.listDevices ?? this.defaults.listDevices) as (
            q: unknown,
          ) => Promise<Record<string, unknown>>;
          const res = await fn({});
          return protoOk(ListDevicesResponseSchema, create(ListDevicesResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/PutDeviceMeta": {
          const req = fromBinary(PutDeviceMetaRequestSchema, body);
          const fn = (this.custom.putDeviceMeta ?? this.defaults.putDeviceMeta) as (
            q: typeof req,
          ) => Promise<Record<string, unknown>>;
          const res = await fn(req);
          return protoOk(PutDeviceMetaResponseSchema, create(PutDeviceMetaResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/ListMachines": {
          const fn = (this.custom.listMachines ?? this.defaults.listMachines) as (
            q: unknown,
          ) => Promise<Record<string, unknown>>;
          const res = await fn({});
          return protoOk(ListMachinesResponseSchema, create(ListMachinesResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/ListRemoteSessions": {
          const req = fromBinary(ListRemoteSessionsRequestSchema, body);
          const fn = (this.custom.listRemoteSessions ?? this.defaults.listRemoteSessions) as (
            q: typeof req,
          ) => Promise<Record<string, unknown>>;
          const res = await fn(req);
          return protoOk(ListRemoteSessionsResponseSchema, create(ListRemoteSessionsResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/GetEvents": {
          const req = fromBinary(GetEventsRequestSchema, body);
          const fn = (this.custom.getEvents ?? this.defaults.getEvents) as (
            q: typeof req,
          ) => Promise<Record<string, unknown>>;
          const res = await fn(req);
          return protoOk(GetEventsResponseSchema, create(GetEventsResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/Forward": {
          const req = fromBinary(ForwardRequestSchema, body);
          const res = await this.invokeMockForward(req, { signal });
          return protoOk(ForwardResponseSchema, create(ForwardResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/GetImage": {
          const req = fromBinary(GetImageRequestSchema, body);
          const fn = this.custom.getImage as ((q: typeof req) => Promise<Record<string, unknown>>) | undefined;
          if (!fn) throw new ConnectError("no such image", Code.NotFound);
          const res = await fn(req);
          return protoOk(GetImageResponseSchema, create(GetImageResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/GetPushKey": {
          const fn = (this.custom.getPushKey ?? this.defaults.getPushKey) as (
            q: unknown,
          ) => Promise<Record<string, unknown>>;
          const res = await fn({});
          return protoOk(GetPushKeyResponseSchema, create(GetPushKeyResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/SetPushSubscription": {
          const req = fromBinary(SetPushSubscriptionRequestSchema, body);
          const fn = (this.custom.setPushSubscription ?? this.defaults.setPushSubscription) as (
            q: typeof req,
          ) => Promise<Record<string, unknown>>;
          const res = await fn(req);
          return protoOk(SetPushSubscriptionResponseSchema, create(SetPushSubscriptionResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/SetPushTarget": {
          const req = fromBinary(SetPushTargetRequestSchema, body);
          const fn = (this.custom.setPushTarget ?? this.defaults.setPushTarget) as (
            q: typeof req,
          ) => Promise<Record<string, unknown>>;
          const res = await fn(req);
          return protoOk(SetPushTargetResponseSchema, create(SetPushTargetResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/GetAccountPrefs": {
          const req = fromBinary(GetAccountPrefsRequestSchema, body);
          const fn = (this.custom.getAccountPrefs ?? this.defaults.getAccountPrefs) as (
            q: typeof req,
          ) => Promise<Record<string, unknown>>;
          const res = await fn(req);
          return protoOk(GetAccountPrefsResponseSchema, create(GetAccountPrefsResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/PutAccountPrefs": {
          const req = fromBinary(PutAccountPrefsRequestSchema, body);
          const fn = (this.custom.putAccountPrefs ?? this.defaults.putAccountPrefs) as (
            q: typeof req,
          ) => Promise<Record<string, unknown>>;
          const res = await fn(req);
          return protoOk(PutAccountPrefsResponseSchema, create(PutAccountPrefsResponseSchema, res as never));
        }
        case "/remoteagent.v1.AuthService/GetChallenge": {
          const fn = (this.custom.getChallenge ?? this.defaults.getChallenge) as (
            q: unknown,
          ) => Promise<Record<string, unknown>>;
          const res = await fn({});
          return protoOk(GetChallengeResponseSchema, create(GetChallengeResponseSchema, res as never));
        }
        case "/remoteagent.v1.AuthService/Login": {
          const fn = (this.custom.login ?? this.defaults.login) as (
            q: unknown,
          ) => Promise<Record<string, unknown>>;
          const res = await fn({});
          return protoOk(LoginResponseSchema, create(LoginResponseSchema, res as never));
        }
        default:
          return new Response(JSON.stringify({ code: "unimplemented", message: path }), {
            status: 404,
            headers: { "Content-Type": "application/json" },
          });
      }
    } catch (err) {
      if (signal?.aborted || (err instanceof Error && err.name === "AbortError")) {
        throw err;
      }
      if (err instanceof ConnectError) {
        const details: Array<{ type: string; value: string }> = [];
        for (const d of err.details ?? []) {
          let type = "";
          let bytes: Uint8Array | undefined;
          if ("type" in d && "value" in d && d.value instanceof Uint8Array) {
            type = d.type;
            bytes = d.value;
          } else if ("desc" in d && d.desc && "value" in d) {
            type = d.desc.typeName;
            bytes = toBinary(d.desc, create(d.desc, d.value as never));
          }
          if (type && bytes) {
            let bin = "";
            for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
            details.push({ type, value: btoa(bin) });
          }
        }
        return new Response(
          JSON.stringify({
            code: CONNECT_CODE_NAMES[err.code] ?? "unknown",
            message: err.rawMessage || err.message,
            ...(details.length > 0 ? { details } : {}),
          }),
          {
            status: 400,
            headers: {
              "Content-Type": "application/json",
              ...(this.mockRelayEpoch ? { "Remote-Agent-Relay-Epoch": this.mockRelayEpoch } : {}),
            },
          },
        );
      }
      return new Response(
        JSON.stringify({ code: "internal", message: err instanceof Error ? err.message : String(err) }),
        { status: 500, headers: { "Content-Type": "application/json" } },
      );
    }
  }

  get machine() {
    return this.world.machine;
  }

  async bootstrapSecurity() {
    return getCoreHost(this).securityClient.bootstrap();
  }

  async syncSecurityNow(force = false) {
    return getCoreHost(this).securityClient.syncNow(force);
  }

  async seal<D extends DescMessage>(
    header: Fields,
    schema: D,
    msg: MessageShape<D>,
    seed?: B,
  ) {
    const h = { ...header, machineId: header.machineId ?? this.machine };
    return this.world.fixture.seal(
      this.machine,
      h,
      new Uint8Array(toBinary(schema, msg)),
      seed ?? ([1, 7, 11].includes(h.kind ?? 0) ? this.seed : undefined),
    );
  }

  async sessionRow(id: string, title = "synthetic session", seed = this.seed) {
    return {
      sessionId: id,
      machineId: this.machine,
      key: await this.world.fixture.seal(
        this.machine,
        { kind: 3, machineId: this.machine, sessionId: id, seq: 1n },
        seed,
      ),
      meta: await this.seal(
        { kind: 2, sessionId: id, seq: 1n },
        SessionSchema,
        create(SessionSchema, { id, title, status: SessionStatus.IDLE }),
      ),
      lastSeq: 0n,
      updatedMs: 0n,
      endedMs: 0n,
    };
  }

  async seedSession(sessionId: string, seed = this.seed) {
    const keyEnv = await this.world.fixture.seal(
      this.machine,
      { kind: 3, machineId: this.machine, sessionId, seq: 1n },
      seed,
    );
    await this.bootstrapSecurity();
    await getCoreHost(this).securityOp({
      case: "openEnvelope",
      value: create(SecurityOpenEnvelopeOpSchema, {
        rawEnvelope: toBinary(EnvelopeSchema, keyEnv) as Uint8Array<ArrayBuffer>,
        expectedPurpose: Purpose.SessionKey,
        machineId: this.machine,
        sessionId,
        history: true,
      }),
    });
  }

  static async create() {
    const world = await GoSecurityFixture.create();
    const member = await webMember(world);
    const seed = crypto.getRandomValues(new Uint8Array(32));
    const r = new SyntheticRemote(world, member, seed);
    await r.bootstrapSecurity();
    const machineMeta = await r.seal(
      { kind: 4, seq: 1n },
      MachineMetaSchema,
      create(MachineMetaSchema, {
        hostname: "synthetic-machine",
        protocolVersion: 10,
      }),
    );
    (r as unknown as { api: object }).api = {
      listMachines: async () => ({
        machines: [
          {
            id: r.machine,
            online: true,
            lastSeenMs: 0n,
            meta: machineMeta,
          },
        ],
      }),
    };
    await r.seedSession("s", seed);
    patchSyncState(r, {
      sessions: new Map([
        [
          "s",
          {
            id: "s",
            machineId: r.machine,
            metaRev: 0n,
            lastSeq: 0n,
            updatedMs: 0n,
            endedMs: 0n,
          },
        ],
      ]),
    });
    return r;
  }

  async addClient() {
    const member = await webMember(this.world);
    const c = new SyntheticRemote(this.world, member, this.seed);
    await c.bootstrapSecurity();
    return c;
  }

  async dispose() {
    super.dispose();
    await this.world.fixture.close();
  }
}
