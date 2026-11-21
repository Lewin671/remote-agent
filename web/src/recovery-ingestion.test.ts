import { fromBinary, create, toBinary } from "@bufbuild/protobuf";
import { expect, it, vi } from "vitest";
import { getCoreHost } from "./core/host";
import { renderText } from "./core/text";
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
import {
  DeviceMetaSchema,
  MachineMetaSchema,
  ProjectPrefsSchema,
} from "./gen/remoteagent/v1/remote_pb";
import { SecurityOpenEnvelopeOpSchema } from "./gen/remoteagent/core/v1/security_pb";
import { Purpose, SecurityError } from "./device-security";
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
  const oldClientMeta = await world.fixture.seal(
    retained.account.deviceId,
    {
      kind: 13,
      requestId: retained.account.deviceId,
      seq: 1n,
      sender: retained.account.deviceId,
    },
    toBinary(
      DeviceMetaSchema,
      create(DeviceMetaSchema, { name: "offline-client" }),
    ),
  );
  const oldPrefs = await world.fixture.seal(
    retained.account.deviceId,
    {
      kind: 12,
      requestId: "project-prefs",
      seq: 1n,
      sender: retained.account.deviceId,
    },
    toBinary(ProjectPrefsSchema, create(ProjectPrefsSchema, { schema: 1 })),
  );
  await world.fixture.recover();
  const member = await webMember(world);
  const r = new SyntheticRemote(world, member, seed);
  await r.seedSession("s", seed);
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
  const after = await r.seal(
    { kind: 1, sessionId: "s", seq: 2n },
    EventSchema,
    event(2n, "readable suffix"),
  );
  const fetchEvents = async (rows: Array<{ seq: bigint; envelope: Envelope }>) => {
    const row = await r.sessionRow("s", "rec", seed);
    row.lastSeq = rows.reduce((m, item) => (item.seq > m ? item.seq : m), 0n);
    const local = r as unknown as {
      token: string;
      tokenExpires: number;
      api: object;
      openStreamLeg: (id: bigint, t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) => void;
    };
    local.token = "tok";
    local.tokenExpires = Date.now() + 7 * 86_400_000;
    local.api = {
      listMachines: async () => ({
        machines: [{ id: world.machine, online: true, lastSeenMs: 0n }],
      }),
      listRemoteSessions: async () => ({ sessions: [row] }),
      listDevices: async () => ({ devices: [] }),
      getAccountPrefs: async () => ({}),
      getEvents: async () => ({ events: rows, more: false }),
    };
    local.openStreamLeg = (legId, _t, cb) => cb.onReady(legId, BigInt(Date.now()));
    r.start();
    const host = getCoreHost(r);
    host.watchSession("s");
    host.sessionOpen("s");
    return host;
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
    const host = await f.fetchEvents([
      { seq: 1n, envelope: f.before },
      { seq: 2n, envelope: f.after },
    ]);
    await vi.waitFor(() => {
      const timeline = host.viewSession("s").timeline;
      expect(timeline?.loaded).toBe(true);
      expect(timeline?.items.length).toBe(1);
    });
    const timeline = host.viewSession("s").timeline!;
    expect(renderText(timeline.error)).toBe("");
    expect(timeline.unavailableCount).toBe(1);
    expect(timeline.historyPartial).toBe(true);
    expect(timeline.items[0]?.kind).toMatchObject({
      case: "agentMessage",
      value: { markdown: "readable suffix" },
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
      const seq = damage === "signature" ? 1n : 2n;
      let err: unknown;
      try {
        await (
          f.r as unknown as {
            ensureHost(): {
              securityOp(op: {
                case: "openEnvelope";
                value: ReturnType<typeof create<typeof SecurityOpenEnvelopeOpSchema>>;
              }): Promise<unknown>;
            };
          }
        )
          .ensureHost()
          .securityOp({
            case: "openEnvelope",
            value: create(SecurityOpenEnvelopeOpSchema, {
              envelope,
              expectedPurpose: Purpose.Event,
              machineId: f.world.machine,
              sessionId: "s",
              seq,
              history: true,
              overrideSessionSeed: f.seed,
            }),
          });
      } catch (e) {
        err = e;
      }
      expect(err).toBeInstanceOf(SecurityError);
      expect((err as SecurityError).code).not.toBe("unavailable");

      const host = await f.fetchEvents(
        damage === "signature"
          ? [{ seq: 1n, envelope }]
          : [{ seq: 1n, envelope: f.before }, { seq: 2n, envelope }],
      );
      await vi.waitFor(() => {
        const timeline = host.viewSession("s").timeline;
        expect(renderText(timeline?.error)).not.toBe("");
      });
      const timeline = host.viewSession("s").timeline!;
      if (damage === "signature") {
        expect(timeline.unavailableCount).toBe(0);
      }
      expect(timeline.items).toHaveLength(0);
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
  const retainedRemote = new SyntheticRemote(f.world, f.retained, f.seed);
  try {
    await f.retained.sdk.sync();
    const internal = retainedRemote as unknown as {
      ensureSecurityRecordLoaded(): Promise<void>;
      ensureHost(): { securityOp(op: { case: "openEnvelope"; value: ReturnType<typeof create<typeof SecurityOpenEnvelopeOpSchema>> }): Promise<{ result: { case: string; value?: { plaintext: Uint8Array } } }> };
    };
    await internal.ensureSecurityRecordLoaded();
    const opened = await internal.ensureHost().securityOp({
      case: "openEnvelope",
      value: create(SecurityOpenEnvelopeOpSchema, {
        envelope: f.before,
        expectedPurpose: Purpose.Event,
        machineId: f.world.machine,
        sessionId: "s",
        history: true,
        overrideSessionSeed: f.seed,
      }),
    });
    if (opened.result.case !== "opened" || !opened.result.value) throw new Error("expected opened envelope");
    const msg = fromBinary(EventSchema, opened.result.value.plaintext);
    expect(msg.kind).toMatchObject({
      case: "agentMessage",
      value: { text: "before recovery" },
    });
  } finally {
    retainedRemote.stop();
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
