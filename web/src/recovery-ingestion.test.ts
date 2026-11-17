import { create, toBinary } from "@bufbuild/protobuf";
import { expect, it, vi } from "vitest";
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
import {
  DeviceMetaSchema,
  MachineMetaSchema,
  ProjectPrefsSchema,
} from "./gen/remoteagent/v1/remote_pb";
import { RpcEffectSchema } from "./gen/remoteagent/core/v1/app_pb";
import { patchSyncState, SyntheticRemote } from "./test/remote-fixture";
import {
  GoSecurityFixture,
  webMember,
} from "./test/security-fixture";
import type { Envelope } from "./gen/remoteagent/v1/envelope_pb";

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
  const oldPrefs = await retained.sdk.seal(
    {
      kind: 12,
      purpose: 12,
      machine: "",
      session: "",
      request: "project-prefs",
      seq: 1n,
      deadline: 0n,
      compression: 0,
    },
    toBinary(ProjectPrefsSchema, create(ProjectPrefsSchema, { schema: 1 })),
  );
  await world.fixture.recover();
  const member = await webMember(world);
  const r = new SyntheticRemote(world, member, seed);
  patchSyncState(r, {
    sessions: new Map([
      [
        "s",
        {
          id: "s",
          machineId: world.machine,
          metaRev: 0n,
          lastSeq: 0n,
          updatedMs: 0n,
          endedMs: 0n,
        },
      ],
    ]),
  });
  (r as unknown as { sessionSeeds: Map<string, Uint8Array<ArrayBuffer>> }).sessionSeeds.set("s", seed);
  const after = await r.seal(
    { kind: 1, sessionId: "s", seq: 2n },
    EventSchema,
    event(2n, "readable suffix"),
  );
  const fetchEvents = async (rows: Array<{ seq: bigint; envelope: Envelope }>) => {
    (r as unknown as { api: object }).api = {
      getEvents: async () => ({ events: rows, more: false }),
    };
    return r.executeRpc(
      create(RpcEffectSchema, {
        opId: 1n,
        kind: { case: "getEvents", value: { sessionId: "s", afterSeq: 0n, limit: 50 } },
      }),
    );
  };
  return {
    r,
    world,
    retained,
    before,
    after,
    oldMachineMeta,
    oldClientMeta,
    oldPrefs,
    seed,
    event,
    fetchEvents,
  };
}
it("advances over verified unavailable history and continues with the readable suffix", async () => {
  const f = await setup();
  try {
    const res = await f.fetchEvents([
      { seq: 1n, envelope: f.before },
      { seq: 2n, envelope: f.after },
    ]);
    expect(res.event.case).toBe("eventsPageFetched");
    if (res.event.case !== "eventsPageFetched") throw new Error("unexpected event");
    expect(res.event.value.error).toBe("");
    expect(res.event.value.events.map((e) => e.seq)).toEqual([1n, 2n]);
    expect(res.event.value.events[0].unavailable).toBe(true);
    expect(res.event.value.events[1].event?.kind).toMatchObject({
      case: "agentMessage",
      value: { text: "readable suffix" },
    });
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
      vi.spyOn(console, "warn").mockImplementation(() => undefined);
      const res = await f.fetchEvents([{ seq: damage === "signature" ? 1n : 2n, envelope }]);
      expect(res.event.case).toBe("eventsPageFetched");
      if (res.event.case !== "eventsPageFetched") throw new Error("unexpected event");
      expect(res.event.value.events).toHaveLength(1);
      expect(res.event.value.events[0].broken).toBe(true);
      expect(res.event.value.events[0].unavailable).toBe(false);
    } finally {
      await f.r.dispose();
    }
  }, 20000);
it("keeps signed members when one offline member's metadata predates recovery", async () => {
  const f = await setup();
  try {
    (f.r as unknown as { token: string; tokenExpires: number; api: object; openStreamLeg: unknown }).token = "tok";
    (f.r as unknown as { tokenExpires: number }).tokenExpires = Date.now() + 7 * 86_400_000;
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
      listRemoteSessions: async () => ({ sessions: [] }),
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
      getAccountPrefs: async () => ({}),
    };
    (f.r as unknown as { openStreamLeg: (id: bigint, t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) => void }).openStreamLeg = (legId, _t, cb) =>
      cb.onReady(legId, BigInt(Date.now()));
    f.r.start();
    await vi.waitFor(() => expect(f.r.machines.has(f.world.machine)).toBe(true));
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

it("keeps directory and stored prefs readable when an expired lease defers recovery republishing", async () => {
  const f = await setup();
  const r = new SyntheticRemote(f.world, f.retained, f.seed);
  const put = vi.fn(async () => ({ applied: true }));
  try {
    (r as unknown as { api: object; token: string; tokenExpires: number }).token = "tok";
    (r as unknown as { tokenExpires: number }).tokenExpires = Date.now() + 80 * 60 * 60 * 1000;
    (r as unknown as { api: object }).api = {
      listMachines: async () => ({ machines: [] }),
      listRemoteSessions: async () => ({ sessions: [] }),
      listDevices: async () => ({ devices: [] }),
      getAccountPrefs: async () => ({
        object: {
          objectId: "project-prefs",
          revision: 1n,
          envelope: f.oldPrefs,
        },
      }),
      putAccountPrefs: put,
    };
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(Date.now() + 73 * 60 * 60 * 1000);
    (r as unknown as { tokenExpires: number }).tokenExpires = Date.now() + 7 * 86_400_000;
    await r.loadDevices(false);
    expect(r.devicesStatus).toBe("ready");
    expect(r.devices.has(f.retained.account.deviceId)).toBe(true);
    (r as unknown as { openStreamLeg: (id: bigint, t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) => void }).openStreamLeg = (legId, _t, cb) => {
      cb.onReady(legId, BigInt(Date.now()));
    };
    r.start();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    await r.changePrefs("pin", "/synthetic/expired");
    expect(put).not.toHaveBeenCalled();
  } finally {
    vi.useRealTimers();
    r.stop();
    await f.r.dispose();
  }
}, 20000);

it("retries exhausted recovery CAS work on the same successfully installed head", async () => {
  const f = await setup();
  const r = new SyntheticRemote(f.world, f.retained, f.seed);
  let object = {
    objectId: "project-prefs",
    revision: 1n,
    envelope: f.oldPrefs,
  };
  let calls = 0;
  try {
    const local = r as any;
    local.closed = false;
    local.api = {
      getAccountPrefs: async () => ({ object }),
      putAccountPrefs: async (req: any) => {
        calls++;
        if (calls <= 5) return { applied: false };
        object = { ...object, revision: 2n, envelope: req.envelope };
        return { applied: true };
      },
    };
    await r.loadDevices(false);
    expect(calls).toBe(5);
    await local.syncSecurity();
    expect(calls).toBe(6);
    expect(local.recoveryObjectsPending).toBe(false);
  } finally {
    r.stop();
    await f.r.dispose();
  }
}, 20000);
