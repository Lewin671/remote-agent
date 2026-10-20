// The device directory comes from the signed membership state: a relay's
// listing can only name members, and sealed metadata is verified on open.
import { create, toBinary } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import { DeviceMetaSchema } from "./gen/remoteagent/v1/remote_pb";
import { DeviceKind, DeviceSchema } from "./gen/remoteagent/v1/relay_pb";
import { Purpose } from "./device-security";
import type { Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { SyntheticRemote } from "./test/remote-fixture";

const live: SyntheticRemote[] = [];
afterEach(async () => {
  for (const r of live.splice(0)) await r.dispose();
  vi.restoreAllMocks();
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
  // A forged device push cannot add it either: membership is signed.
  await (r as unknown as { onFrame(frame: unknown): Promise<void> }).onFrame({
    case: "deviceUpdated",
    value: {
      device: create(DeviceSchema, { id: rogue, kind: DeviceKind.CLIENT }),
    },
  });
  expect(r.devices.has(rogue)).toBe(false);
}, 20000);

it("keeps a newer self-authored name and refuses an older listing", async () => {
  const r = await probe();
  const self = r.member.account.deviceId;
  const meta = async (rev: bigint, name: string) =>
    r.member.sdk.seal(
      {
        kind: 13,
        purpose: Purpose.DeviceMeta,
        machine: "",
        session: "",
        request: self,
        seq: rev,
        deadline: 0n,
        compression: 0,
      },
      toBinary(
        DeviceMetaSchema,
        create(DeviceMetaSchema, { name, platform: "web" }),
      ),
    );
  const first = await meta(1n, "old name");
  const second = await meta(2n, "new name");
  directory(r, [{ id: self, meta: second, revision: 2n }]);
  await r.loadDevices(false);
  expect(r.devices.get(self)?.meta?.name).toBe("new name");
  expect(r.devices.get(self)?.metaRev).toBe(2n);
  // An older copy must not replace what is already shown.
  directory(r, [{ id: self, meta: first, revision: 1n }]);
  await r.loadDevices(false);
  expect(r.devices.get(self)?.meta?.name).toBe("new name");
  expect(r.devices.get(self)?.metaRev).toBe(2n);
}, 20000);

it("retries the directory when a new security head outlives a failed refresh", async () => {
  const r = await probe();
  directory(r, [{ id: r.machine, kind: DeviceKind.MACHINE }]);
  await r.loadDevices(false);
  expect(r.devices.has(r.machine)).toBe(true);
  await r.world.fixture.remove([r.machine]);
  let attempts = 0;
  (r as unknown as { api: object }).api = {
    listDevices: async () => {
      if (++attempts === 1)
        throw new Error("synthetic transient directory failure");
      return { devices: [] };
    },
  };
  const poll = () =>
    (r as unknown as { syncSecurity(): Promise<void> }).syncSecurity();
  await poll();
  expect(attempts).toBe(1);
  await poll();
  expect(attempts).toBe(2);
  expect(r.devices.has(r.machine)).toBe(false);
  expect(r.devicesStatus).toBe("ready");
  await poll();
  expect(attempts).toBe(2);
}, 20000);

it("preserves a verified handoff offer when renaming this device", async () => {
  const r = await probe(), self = r.member.account.deviceId;
  const handoffRecoveryEd = crypto.getRandomValues(new Uint8Array(32));
  let revision = 1n;
  let envelope = await r.member.sdk.seal({ kind: 13, purpose: Purpose.DeviceMeta,
    machine: "", session: "", request: self, seq: revision, deadline: 0n, compression: 0 },
    toBinary(DeviceMetaSchema, create(DeviceMetaSchema, { name: "before", platform: "web", handoffRecoveryEd })));
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
