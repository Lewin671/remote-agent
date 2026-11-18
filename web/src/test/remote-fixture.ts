import {
  create,
  fromBinary,
  toBinary,
  type DescMessage,
  type MessageShape,
} from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { unpackSecurity } from "../device-security-client";
import { parseHeader, requestIdentity } from "../device-security";
import { type Envelope } from "../gen/remoteagent/v1/envelope_pb";
import { Remote } from "../remote";
import { GoSecurityFixture, webMember } from "./security-fixture";
import { type Header } from "../gen/remoteagent/v1/envelope_pb";
import { CommandResultSchema, MachineMetaSchema } from "../gen/remoteagent/v1/remote_pb";
import { SessionSchema } from "../gen/remoteagent/v1/local_pb";
import { SessionStatus } from "../gen/remoteagent/v1/event_pb";
import { ConnState as CoreConnState } from "../gen/remoteagent/core/v1/home_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import {
  SyncDeviceViewSchema,
  SyncMachineViewSchema,
  SyncSessionViewSchema,
  SyncStateViewSchema,
} from "../gen/remoteagent/core/v1/sync_view_pb";
import type { DeviceInfo } from "../devices";
import type { ConnState, MachineView, PrefsState, SessionView } from "../remote";

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
  if ((patch.machines || patch.sessions || patch.devices || patch.moreSessions !== undefined) && (demo as unknown as { demoStream?: unknown }).demoStream) {
    (demo as unknown as { syncDemoToCore(forceRefresh?: boolean): void }).syncDemoToCore(patch.moreSessions !== undefined);
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
      connectionReason: patch.connError !== undefined
        ? (patch.connError ? create(TextSchema, { literal: patch.connError }) : undefined)
        : (r.connError ? create(TextSchema, { literal: r.connError }) : undefined),
      liveLoaded: patch.everOnline ?? r.everOnline,
      hasMoreSessions: patch.moreSessions ?? r.moreSessions,
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

type B = Uint8Array<ArrayBuffer>;
type Fields = Omit<Partial<Header>, "$typeName">;
export class SyntheticRemote extends Remote {
  protected override relayEpoch() {return Promise.resolve("fixture-epoch");}
  private custom: Record<string, unknown> = {};
  private readonly defaults: Record<string, unknown>;
  readonly seed: B;
  constructor(
    readonly world: Awaited<ReturnType<typeof GoSecurityFixture.create>>,
    readonly member: Awaited<ReturnType<typeof webMember>>,
    seed: B,
  ) {
    super(member.account, undefined, member.storage);
    this.seed = seed;
    // This synthetic member uses in-memory security storage, with no durable operations.
    this.authority.pending = async () => undefined;
    this.defaults = {
      getEvents: async () => ({ events: [], more: false }),
      listDevices: async () => ({ devices: [] }),
      listMachines: async () => ({
        machines: [{ id: this.machine, online: true, lastSeenMs: 0n }],
      }),
      listRemoteSessions: async () => ({ sessions: [] }),
      putDeviceMeta: async () => ({ applied: true }),
      putAccountPrefs: async () => ({ applied: true }),
      getAccountPrefs: async () => ({}),
      getSecurityStates: async (req: { afterEpoch: bigint; limit: number }) =>
        world.fixture.fetcher(member.account.deviceId)(
          req.afterEpoch,
          req.limit,
        ),
    };
    Object.defineProperty(this, "api", {
      configurable: true,
      get: () => ({
        ...this.defaults,
        ...this.custom,
        forward: async (req: unknown) => {
          const forward = this.custom.forward as
            ((req: unknown) => Promise<unknown>) | undefined;
          if (forward) {
            const request = req as {command:Envelope;attemptId:string};
            const res = await forward(req) as {result?:Envelope;outcome?:string;reason?:string;attemptId?:string};
            try {
              if (res.result) {
                const original = await this.world.fixture.open(this.machine,request.command);
                const opened = await this.world.fixture.open(this.member.account.deviceId,res.result);
                const proof = fromBinary(CommandResultSchema,unpackSecurity(parseHeader(new Uint8Array(res.result.securityHeader)),opened.plaintext));
                if (!proof.outcome) {
                  const binding=await requestIdentity(parseHeader(new Uint8Array(request.command.securityHeader)),unpackSecurity(parseHeader(new Uint8Array(request.command.securityHeader)),original.plaintext));
                  proof.outcome="executed"; proof.reason="completed"; proof.resultAvailable=true;
                  proof.requestKeyHash=binding.key; proof.payloadHash=binding.payload;
                  res.result=await this.seal({...opened.header,compression:0},CommandResultSchema,proof);
                }
                res.outcome ??= proof.outcome; res.reason ??= proof.reason; res.attemptId ??= request.attemptId;
              }
            } catch { /* Keep deliberate signature/ciphertext corruption. */ }
            return res;
          }
          throw new ConnectError(
            "synthetic endpoint offline",
            Code.Unavailable,
          );
        },
      }),
      set: (api: Record<string, unknown>) => {
        this.custom = api;
      },
    });
  }
  get machine() {
    return this.world.machine;
  }
  async seal<D extends DescMessage>(
    header: Fields,
    schema: D,
    msg: MessageShape<D>,
    seed?: B,
  ) {
    const h = { ...header, machineId: header.machineId ?? this.machine };
    const wire = await this.world.fixture.seal(
      this.machine,
      h,
      new Uint8Array(toBinary(schema, msg)),
      seed ?? ([1, 7, 11].includes(h.kind ?? 0) ? this.seed : undefined),
    );
    return wire;
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
  static async create() {
    const world = await GoSecurityFixture.create();
    const member = await webMember(world);
    const seed = crypto.getRandomValues(new Uint8Array(32));
    const r = new SyntheticRemote(world, member, seed);
    const identity = await member.sdk.localIdentity();
    (r as unknown as { trustedMembers: Map<string, unknown> }).trustedMembers =
      new Map(identity.state.core.members.map((m) => [m.id, m]));
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
    (r as unknown as { sessionSeeds: Map<string, B> }).sessionSeeds.set(
      "s",
      seed,
    );
    return r;
  }
  async addClient() {
    const member = await webMember(this.world);
    return new SyntheticRemote(this.world, member, this.seed);
  }
  async dispose() {
    super.dispose();
    await this.world.fixture.close();
  }
}
