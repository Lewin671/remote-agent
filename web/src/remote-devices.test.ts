// The device directory comes from the signed membership state: a relay's
// listing can only name members, and sealed metadata is verified on open.
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import { getCoreHost } from "./core/host";
import { DeviceMetaSchema } from "./gen/remoteagent/v1/remote_pb";
import { DeviceKind, DeviceSchema, MachineSchema } from "./gen/remoteagent/v1/relay_pb";
import type { Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { SyntheticRemote } from "./test/remote-fixture";

const live: SyntheticRemote[] = [];
afterEach(async () => {
  for (const r of live.splice(0)) await r.dispose();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function probe() {
  const r = await SyntheticRemote.create();
  live.push(r);
  return r;
}

it("ignores a device that is not in the signed member roster", async () => {
  const r = await probe();
  const rogue = "ffffffffffffffffffffffffffffffff";
  (r as unknown as { api: object }).api = {
    listMachines: async () => ({
      machines: [
        create(MachineSchema, { id: r.machine, online: true, lastSeenMs: 0n }),
      ],
    }),
    listRemoteSessions: async () => ({ sessions: [] }),
    getAccountPrefs: async () => ({}),
    listDevices: async () => ({
      devices: [
        create(DeviceSchema, { id: r.member.account.deviceId, kind: DeviceKind.CLIENT }),
        create(DeviceSchema, { id: r.machine, kind: DeviceKind.MACHINE }),
        create(DeviceSchema, { id: rogue, kind: DeviceKind.CLIENT }),
      ],
    }),
  };
  (r as any).token = "tok";
  (r as any).tokenExpires = Date.now() + 7 * 86_400_000;
  (r as any).openStreamLeg = (legId: bigint, _t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) =>
    cb.onReady(legId, BigInt(Date.now()));
  r.start();
  const host = getCoreHost(r);
  await vi.waitFor(() => {
    expect(host.viewDevices().clients.map((c) => c.deviceId)).toContain(r.member.account.deviceId);
    expect(host.viewSettings().machines.map((m) => m.machineId)).toContain(r.machine);
  });
  expect(host.viewDevices().clients.map((c) => c.deviceId)).not.toContain(rogue);
  expect(host.viewSettings().machines.find((m) => m.machineId === r.machine)?.inDeviceDirectory).toBe(true);
}, 20000);

it("preserves a verified handoff offer when renaming this device", async () => {
  const r = await probe(), self = r.member.account.deviceId;
  const handoffRecoveryEd = crypto.getRandomValues(new Uint8Array(32));
  let revision = 1n;
  let envelope = await r.world.fixture.seal(
    self,
    { kind: 13, requestId: self, seq: revision, sender: self },
    toBinary(DeviceMetaSchema, create(DeviceMetaSchema, { name: "before", platform: "web", handoffRecoveryEd })),
  );
  (r as unknown as { api: object }).api = {
    listDevices: async () => ({ devices: [create(DeviceSchema, { id: self, kind: DeviceKind.CLIENT, meta: envelope, metaRevision: revision })] }),
    putDeviceMeta: async (req: { expectedRevision: bigint; meta: Envelope }) => {
      expect(req.expectedRevision).toBe(revision);
      envelope = req.meta; revision++;
      return { applied: true };
    },
  };
  await r.renameDevice(self, "after");
  await r.loadDevices(false);
  const opened = await r.world.fixture.open(self, envelope);
  const decoded = fromBinary(DeviceMetaSchema, opened.plaintext);
  expect(decoded.name).toBe("after");
  expect(decoded.handoffRecoveryEd).toEqual(handoffRecoveryEd);
  expect(getCoreHost(r).viewClientDetail(self).displayName).toBe("after");
  expect(() => r.renameDevice(r.machine, "other")).toThrow("名称只能在那台设备上修改");
}, 20000);

it("always fetches a fresh device directory without caching stale entries", async () => {
  const r = await probe();
  const listDevices = vi.fn(async () => ({ devices: [] }));
  (r as any).api = { listDevices };
  await r.loadDevices(false);
  await r.loadDevices(false);
  expect(listDevices).toHaveBeenCalledTimes(2);
}, 20000);

it("describes a newly paired browser without metadata", async () => {
  vi.stubGlobal("navigator", { userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/140.0 Safari/537.36" });
  const r = await probe(), self = r.member.account.deviceId;
  let meta: Envelope | undefined;
  let revision = 0n;
  const putDeviceMeta = vi.fn(async (req: { deviceId: string; expectedRevision: bigint; meta: Envelope }) => {
    expect(req.deviceId).toBe(self);
    expect(req.expectedRevision).toBe(revision);
    meta = req.meta;
    revision++;
    return { applied: true };
  });
  (r as any).token = "tok";
  (r as any).tokenExpires = Date.now() + 7 * 86_400_000;
  (r as any).api = {
    listMachines: async () => ({ machines: [] }),
    listRemoteSessions: async () => ({ sessions: [] }),
    getAccountPrefs: async () => ({}),
    listDevices: async () => ({ devices: [create(DeviceSchema, { id: self, kind: DeviceKind.CLIENT, meta, metaRevision: revision })] }),
    putDeviceMeta,
  };
  (r as any).openStreamLeg = (legId: bigint, _t: string, cb: { onReady: (id: bigint, ms: bigint) => void }) =>
    cb.onReady(legId, BigInt(Date.now()));
  r.start();
  await vi.waitFor(() => expect(putDeviceMeta).toHaveBeenCalledTimes(1));
  await vi.waitFor(() => {
    expect(getCoreHost(r).viewClientDetail(self).description).not.toBe("");
  });
  expect(revision).toBe(1n);
  await r.loadDevices();
  expect(putDeviceMeta).toHaveBeenCalledTimes(1);
}, 10000);
