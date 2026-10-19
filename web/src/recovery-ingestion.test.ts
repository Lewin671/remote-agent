import { create, toBinary } from "@bufbuild/protobuf";
import { afterAll, expect, it } from "vitest";
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
import {
  DeviceMetaSchema,
  MachineMetaSchema,
} from "./gen/remoteagent/v1/remote_pb";
import { SyntheticRemote } from "./test/remote-fixture";
import {
  GoSecurityFixture,
  webMember,
  removeFixtureBuild,
} from "./test/security-fixture";
import { importSessionKey } from "./crypto";
import type { Envelope } from "./gen/remoteagent/v1/envelope_pb";

afterAll(removeFixtureBuild);
async function setup() {
  const world = await GoSecurityFixture.create();
  const seed = crypto.getRandomValues(new Uint8Array(32));
  const event = (seq: bigint, text: string) =>
    create(EventSchema, {
      sessionId: "s",
      seq,
      kind: { case: "agentMessage", value: { text } },
    });
  const before = await world.fixture.seal(
    world.machine,
    { kind: 1, machineId: world.machine, sessionId: "s", seq: 1n },
    toBinary(EventSchema, event(1n, "before recovery")),
    seed,
  );
  const oldMachineMeta = await world.fixture.seal(
    world.machine,
    { kind: 4, machineId: world.machine, seq: 1n },
    toBinary(
      MachineMetaSchema,
      create(MachineMetaSchema, { hostname: "offline-machine" }),
    ),
  );
  const retained = await webMember(world);
  const oldClientMeta = await retained.sdk.seal(
    {
      kind: 13,
      purpose: 13,
      machine: "",
      session: "",
      request: retained.account.deviceId,
      seq: 1n,
      deadline: 0n,
      compression: 0,
    },
    toBinary(
      DeviceMetaSchema,
      create(DeviceMetaSchema, { name: "offline-client" }),
    ),
  );
  await world.fixture.recover();
  const member = await webMember(world);
  const r = new SyntheticRemote(world, member, seed);
  r.sessions.set("s", {
    id: "s",
    machineId: world.machine,
    key: await importSessionKey(seed),
    metaRev: 0n,
    lastSeq: 0n,
    updatedMs: 0n,
    endedMs: 0n,
  });
  const local = r as unknown as {
    sessionSeeds: Map<string, Uint8Array<ArrayBuffer>>;
    buffers: Map<string, unknown>;
    ingest(
      s: unknown,
      stored: unknown[],
      fill: boolean,
      cached: boolean,
      history: boolean,
    ): Promise<void>;
  };
  local.sessionSeeds.set("s", seed);
  // Directly exercise the same ingestion method used by GetEvents and pushes.
  local.buffers.set("s", {
    events: [],
    pending: new Map(),
    fetching: false,
    refetch: false,
    loaded: false,
    restored: true,
    error: "",
    broken: false,
    bytes: 0,
  });
  const after = await r.seal(
    { kind: 1, sessionId: "s", seq: 2n },
    EventSchema,
    event(2n, "readable suffix"),
  );
  return {
    r,
    world,
    retained,
    before,
    after,
    oldMachineMeta,
    oldClientMeta,
    seed,
    event,
    ingest: async (seq: bigint, envelope: Envelope, history = true) =>
      local.ingest(
        r.sessions.get("s"),
        [{ seq, envelope }],
        false,
        true,
        history,
      ),
  };
}
it("advances over verified unavailable history and continues with the readable suffix and live events", async () => {
  const f = await setup();
  try {
    await f.ingest(1n, f.before);
    await f.ingest(2n, f.after);
    const live = await f.r.seal(
      { kind: 1, sessionId: "s", seq: 3n },
      EventSchema,
      f.event(3n, "new message"),
    );
    await f.ingest(3n, live, false);
    expect(f.r.events("s").map((e) => e.seq)).toEqual([1n, 2n, 3n]);
    expect(f.r.events("s")[0].kind.case).toBeUndefined();
    expect(f.r.events("s")[2].kind).toMatchObject({
      case: "agentMessage",
      value: { text: "new message" },
    });
    expect(f.r.history("s")).toMatchObject({ unavailable: 1, broken: false });
  } finally {
    await f.r.dispose();
  }
}, 20000);
for (const damage of ["signature", "route", "current-cipher"] as const)
  it(`never skips a ${damage} verification failure as unavailable history`, async () => {
    const f = await setup();
    try {
      const original = damage === "current-cipher" ? f.after : f.before;
      const envelope = {
        ...original,
        signature: new Uint8Array(original.signature),
        ciphertext: new Uint8Array(original.ciphertext),
      };
      if (damage === "signature") envelope.signature[0] ^= 1;
      if (damage === "current-cipher") envelope.ciphertext[0] ^= 1;
      await f.ingest(damage === "signature" ? 1n : 2n, envelope);
      expect(f.r.events("s")).toHaveLength(0);
      expect(f.r.history("s").broken).toBe(true);
      expect(f.r.history("s").unavailable).toBe(0);
    } finally {
      await f.r.dispose();
    }
  }, 20000);
it("keeps signed members when one offline member's metadata predates recovery", async () => {
  const f = await setup();
  try {
    (f.r as unknown as { api: object }).api = {
      listMachines: async () => ({
        machines: [
          {
            id: f.world.machine,
            online: false,
            lastSeenMs: 0n,
            meta: f.oldMachineMeta,
          },
        ],
      }),
      listDevices: async () => ({
        devices: [
          {
            id: f.retained.account.deviceId,
            kind: 2,
            meta: f.oldClientMeta,
            metaRevision: 1n,
          },
        ],
      }),
    };
    await f.r.loadDevices(false);
    expect(f.r.devicesStatus).toBe("ready");
    expect(f.r.devices.has(f.world.machine)).toBe(true);
    expect(f.r.machines.has(f.world.machine)).toBe(true);
    expect(f.r.machines.get(f.world.machine)?.meta).toBeUndefined();
    expect(f.r.devices.get(f.retained.account.deviceId)?.meta).toBeUndefined();
  } finally {
    await f.r.dispose();
  }
}, 20000);

it("retained clients still read the old plaintext when their keys are available", async () => {
  const f = await setup();
  try {
    await f.retained.sdk.sync();
    const opened = await f.retained.sdk.openMessage(
      f.before,
      EventSchema,
      true,
      f.seed,
    );
    expect(opened.msg.kind).toMatchObject({
      case: "agentMessage",
      value: { text: "before recovery" },
    });
  } finally {
    await f.r.dispose();
  }
}, 20000);
