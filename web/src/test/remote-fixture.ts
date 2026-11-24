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
import { type Envelope, type Header } from "../gen/remoteagent/v1/envelope_pb";
import { getCoreHost } from "../core/host";
import { Purpose, Remote, type ConnState, type MachineView, type PrefsState, type SessionView } from "../remote";
import { GoSecurityFixture, webMember } from "./security-fixture";
import { CommandResultSchema, MachineMetaSchema } from "../gen/remoteagent/v1/remote_pb";
import { SessionSchema } from "../gen/remoteagent/v1/local_pb";
import { SessionStatus } from "../gen/remoteagent/v1/event_pb";
import {
  GetAccountPrefsResponseSchema,
  GetChallengeResponseSchema,
  GetSecurityStatesRequestSchema,
  GetSecurityStatesResponseSchema,
  ListDevicesResponseSchema,
  ListMachinesResponseSchema,
  LoginResponseSchema,
  PutAccountPrefsResponseSchema,
  PutDeviceMetaRequestSchema,
  PutDeviceMetaResponseSchema,
} from "../gen/remoteagent/v1/relay_pb";
import { ConnState as CoreConnState } from "../gen/remoteagent/core/v1/home_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import {
  SyncDeviceViewSchema,
  SyncMachineViewSchema,
  SyncSessionViewSchema,
  SyncStateViewSchema,
} from "../gen/remoteagent/core/v1/sync_view_pb";
import type { DeviceInfo } from "../devices";

export interface SyncStatePatch {
  conn?: ConnState;
  listed?: boolean;
  machinesListed?: boolean;
  sessionsListed?: boolean;
  everOnline?: boolean;
  moreSessions?: boolean;
  connError?: string;
  removed?: boolean;
  displayGrace?: boolean;
  reachabilityUnknown?: boolean;
  machines?: Map<string, MachineView>;
  sessions?: Map<string, SessionView>;
  devices?: Map<string, DeviceInfo>;
  prefs?: Partial<PrefsState>;
}

export function patchSyncState(r: Remote, patch: SyncStatePatch): void {
  const conn = patch.conn ?? r.conn;
  const removed = patch.removed ?? r.removed;
  const machinesMap = patch.machines ?? r.machines;
  const sessionsMap = patch.sessions ?? r.sessions;
  const demo = r as unknown as {
    demoMachines?: Map<string, MachineView>;
    demoSessions?: Map<string, SessionView>;
    demoDevices?: Map<string, DeviceInfo>;
    demoHasMoreSessions?: boolean;
    opts?: URLSearchParams;
  };
  if (patch.conn === "online" && demo.opts?.has("conn")) {
    demo.opts.delete("conn");
  }
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
  if (
    (patch.machines || patch.sessions || patch.devices || patch.moreSessions !== undefined) &&
    (demo as unknown as { demoStream?: unknown }).demoStream
  ) {
    (demo as unknown as { syncDemoToCore(forceRefresh?: boolean): void }).syncDemoToCore(
      patch.moreSessions !== undefined,
    );
  }
  const devicesMap = patch.devices ?? r.devices;
  const prefsStatus = patch.prefs?.status ?? r.prefs.status;
  const prefsSnap = patch.prefs?.snap ?? r.prefs.snap;
  const pendingSize = patch.prefs?.pending?.size ?? r.prefs.pending.size;
  const coreConn =
    conn === "online"
      ? CoreConnState.CONNECTED
      : conn === "offline"
        ? CoreConnState.OFFLINE
        : CoreConnState.CONNECTING;
  const machinesListed = patch.machinesListed ?? patch.listed ?? r.listed;
  const sessionsListed = patch.sessionsListed ?? patch.listed ?? r.listed;
  r.applySyncStateView(
    create(SyncStateViewSchema, {
      connection: coreConn,
      connectionReason:
        patch.connError !== undefined
          ? patch.connError
            ? create(TextSchema, { literal: patch.connError })
            : undefined
          : r.connError
            ? create(TextSchema, { literal: r.connError })
            : undefined,
      liveLoaded: patch.everOnline ?? r.everOnline,
      hasMoreSessions: patch.moreSessions ?? false,
      removed,
      displayGrace: patch.displayGrace ?? false,
      reachabilityUnknown: patch.reachabilityUnknown ?? false,
      machinesListed,
      sessionsListed,
      machines: [...machinesMap.values()].map((m) =>
        create(SyncMachineViewSchema, {
          machineId: m.id,
          online: m.online,
          lastSeenMs: m.lastSeenMs,
          meta: m.meta,
          metaRev: m.metaRev,
        }),
      ),
      sessions: [...sessionsMap.values()].map((s) =>
        create(SyncSessionViewSchema, {
          sessionId: s.id,
          machineId: s.machineId,
          info: s.info,
          metaRev: s.metaRev,
          lastSeq: s.lastSeq,
          updatedMs: s.updatedMs,
          listedEndedMs: s.endedMs,
          checkpoint: s.checkpoint,
          checkpointInvalid: s.checkpointStatus === "invalid",
        }),
      ),
      devices: [...devicesMap.values()].map((d) =>
        create(SyncDeviceViewSchema, {
          deviceId: d.id,
          kind: d.kind,
          createdMs: d.createdMs,
          lastSeenMs: d.lastSeenMs,
          meta: d.meta,
          metaRev: d.metaRev,
        }),
      ),
      prefsLoaded: prefsStatus === "ready",
      prefsLoadFailed: prefsStatus === "error",
      prefsRevision: prefsSnap?.revision ?? 0n,
      prefs: prefsSnap?.prefs,
      prefsSyncing: pendingSize > 0,
    }),
  );
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
  protected override relayEpoch() {
    return Promise.resolve("fixture-epoch");
  }
  private custom: Record<string, unknown> = {};
  private readonly defaults: Record<string, unknown>;
  readonly seed: B;

  constructor(
    readonly world: Awaited<ReturnType<typeof GoSecurityFixture.create>>,
    readonly member: Awaited<ReturnType<typeof webMember>>,
    seed: B,
  ) {
    const rpcHolder: { handle?: (url: string, body: Uint8Array) => Promise<Response> } = {};
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
      return rpcHolder.handle!(url, body);
    };
    super(member.account, undefined, undefined, undefined, undefined, member.store, fetchFn);
    this.seed = seed;
    rpcHolder.handle = (url, body) => this.handleMockRpc(url, body);
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
        forward: async (req: unknown) => {
          const forward = this.custom.forward as ((req: unknown) => Promise<unknown>) | undefined;
          if (forward) {
            const request = req as { command: Envelope; attemptId: string };
            const res = (await forward(req)) as {
              result?: Envelope;
              outcome?: string;
              reason?: string;
              attemptId?: string;
            };
            try {
              if (res.result) {
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
            return res;
          }
          throw new ConnectError("synthetic endpoint offline", Code.Unavailable);
        },
      }),
      set: (api: Record<string, unknown>) => {
        this.custom = api;
      },
    });
  }

  private async handleMockRpc(url: string, body: Uint8Array): Promise<Response> {
    const path = new URL(url, "https://ra.example.com").pathname;
    const protoOk = <D extends DescMessage>(schema: D, msg: MessageShape<D>) =>
      new Response(new Uint8Array(toBinary(schema, msg)), {
        status: 200,
        headers: { "Content-Type": "application/proto" },
      });
    try {
      switch (path) {
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
        case "/remoteagent.v1.ClientService/GetAccountPrefs": {
          const fn = (this.custom.getAccountPrefs ?? this.defaults.getAccountPrefs) as (
            q: unknown,
          ) => Promise<Record<string, unknown>>;
          const res = await fn({});
          return protoOk(GetAccountPrefsResponseSchema, create(GetAccountPrefsResponseSchema, res as never));
        }
        case "/remoteagent.v1.ClientService/PutAccountPrefs": {
          const fn = (this.custom.putAccountPrefs ?? this.defaults.putAccountPrefs) as (
            q: unknown,
          ) => Promise<Record<string, unknown>>;
          const res = await fn({});
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
      if (err instanceof ConnectError) {
        return new Response(
          JSON.stringify({
            code: CONNECT_CODE_NAMES[err.code] ?? "unknown",
            message: err.rawMessage || err.message,
          }),
          { status: 400, headers: { "Content-Type": "application/json" } },
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
        envelope: keyEnv,
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
