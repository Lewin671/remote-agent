// The device directory comes from the signed membership state: a relay's
// listing can only name members, and sealed metadata is verified on open.
import { create, toBinary } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import { DeviceMetaSchema } from "./gen/remoteagent/v1/remote_pb";
import { DeviceKind, DeviceSchema } from "./gen/remoteagent/v1/relay_pb";
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
function directory(
  r: SyntheticRemote,
  devices: {
    id: string;
    kind?: DeviceKind;
    meta?: Envelope;
    revision?: bigint;
  }[],
) {
  (r as unknown as { api: object }).api = {
    listDevices: async () => ({
      devices: devices.map((d) =>
        create(DeviceSchema, {
          id: d.id,
          kind: d.kind ?? DeviceKind.CLIENT,
          createdMs: 0n,
          lastSeenMs: 0n,
          meta: d.meta,
          metaRevision: d.revision ?? 0n,
        }),
      ),
    }),
  };
}

it("ignores a device that is not in the signed member roster", async () => {
  const r = await probe();
  const rogue = "ffffffffffffffffffffffffffffffff";
  directory(r, [
    { id: r.member.account.deviceId },
    { id: r.machine, kind: DeviceKind.MACHINE },
    { id: rogue },
  ]);
  await r.loadDevices(false);
  expect(r.devices.has(rogue)).toBe(false);
  expect(r.devices.has(r.member.account.deviceId)).toBe(true);
  expect(r.devices.has(r.machine)).toBe(true);
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
  expect(r.devices.get(self)?.meta?.name).toBe("after");
  expect(r.devices.get(self)?.meta?.handoffRecoveryEd).toEqual(handoffRecoveryEd);
  expect(() => r.renameDevice(r.machine, "other")).toThrow("名称只能在那台设备上修改");
}, 20000);

it("does not reuse a directory while recovery publication or reconciliation is pending", async () => {
  const r = await probe();
  const listDevices = vi.fn(async () => ({ devices: [] }));
  (r as any).api = { listDevices };
  await r.loadDevices(false);
  (r as any).recoveryObjectsPending = true;
  await r.loadDevices(false);
  expect(listDevices).toHaveBeenCalledTimes(2);
  (r as any).reconcilingSecurity = 1;
  await r.loadDevices(false);
  expect(listDevices).toHaveBeenCalledTimes(3);
  (r as any).reconcilingSecurity = 0;
  vi.spyOn(r.authority, "pending").mockResolvedValue({} as never);
  await r.loadDevices(false);
  expect(listDevices).toHaveBeenCalledTimes(4);
}, 20000);

it("describes a newly paired browser without metadata", async () => {
  const r = await probe(), self = r.member.account.deviceId;
  vi.stubGlobal("navigator", { userAgent: "SyntheticBrowser" });
  let meta: Envelope | undefined;
  let revision = 0n;
  const putDeviceMeta = vi.fn(async (req: { deviceId: string; expectedRevision: bigint; meta: Envelope }) => {
    expect(req.deviceId).toBe(self);
    expect(req.expectedRevision).toBe(revision);
    meta = req.meta;
    revision++;
    return { applied: true };
  });
  (r as any).api = {
    listDevices: async () => ({ devices: [create(DeviceSchema, { id: self, kind: DeviceKind.CLIENT, meta, metaRevision: revision })] }),
    putDeviceMeta,
  };
  await r.loadDevices();
  await vi.waitFor(() => expect(putDeviceMeta).toHaveBeenCalledTimes(1));
  await vi.waitFor(() => expect(r.devices.get(self)?.meta?.platform).toBe("web"));
  expect(r.devices.get(self)?.metaRev).toBe(1n);
  expect((r as any).descriptionPending).toBe(false);
  await r.loadDevices();
  expect(putDeviceMeta).toHaveBeenCalledTimes(1);
}, 10000);
