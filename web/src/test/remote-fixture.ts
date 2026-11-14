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
    r.sessions.set("s", {
      id: "s",
      machineId: r.machine,
      metaRev: 0n,
      lastSeq: 0n,
      updatedMs: 0n,
      endedMs: 0n,
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
    this.stop();
    await this.world.fixture.close();
  }
}
