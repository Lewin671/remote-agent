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

function countedDirectory(r: SyntheticRemote) {
  const listDevices = vi.fn(async () => ({ devices: [] }));
  const listMachines = vi.fn(async () => ({ machines: [] }));
  (r as any).api = { listDevices, listMachines };
  return { listDevices, listMachines };
}

it("checks security on every known update without reloading an unchanged directory", async () => {
  const r = await probe(), api = countedDirectory(r);
  await r.loadDevices(false);
  const sync = vi.spyOn(r.security, "sync");
  const open = vi.spyOn(r as any, "openSecureMessage");
  const row = await r.sessionRow("s");
  const frame = (case_: string, value: object) => (r as any).onFrame({ case: case_, value });
  await frame("sessionUpdated", { session: row });
  await frame("sessionUpdated", { session: row });
  await frame("machineUpdated", { machine: { id: r.machine, online: true, lastSeenMs: 1n } });
  expect(sync).toHaveBeenCalledTimes(3);
  expect(api.listDevices).toHaveBeenCalledTimes(1);
  expect(api.listMachines).toHaveBeenCalledTimes(1);
  // Only each pushed session's own payload is opened, never member metadata.
  expect(open.mock.calls.every(([, route]) => (route as any).kind === 2)).toBe(true);
  await r.loadDevices(false);
  expect(api.listDevices).toHaveBeenCalledTimes(2);
}, 20000);

it("fully reloads changed heads, removed or unknown machines, and unknown epochs", async () => {
  const r = await probe(), api = countedDirectory(r);
  await r.loadDevices(false);
  const row = await r.sessionRow("s");
  const update = (value = row) => (r as any).onFrame({ case: "sessionUpdated", value: { session: value } });
  const other = await r.addClient();
  await update();
  expect(api.listDevices).toHaveBeenCalledTimes(2);
  other.stop();
  await update({ ...row, machineId: "unknown-machine" });
  expect(api.listDevices).toHaveBeenCalledTimes(3);
  const { headerBytes, parseHeader } = await import("./device-security");
  const meta = { ...row.meta, securityHeader: headerBytes({ ...parseHeader(new Uint8Array(row.meta.securityHeader)), epoch: 999n }) };
  await update({ ...row, meta });
  expect(api.listDevices).toHaveBeenCalledTimes(4);
  await r.world.fixture.remove([r.machine]);
  await update();
  expect(api.listDevices).toHaveBeenCalledTimes(5);
  expect(r.devices.has(r.machine)).toBe(false);
  await update();
  expect(api.listDevices).toHaveBeenCalledTimes(6);
  expect(r.devices.has(r.machine)).toBe(false);
}, 20000);

it("coalesces directory loads and does not acknowledge a head changed during loading", async () => {
  const r = await probe();
  let release!: () => void, entered!: () => void;
  const started = new Promise<void>(resolve => { entered = resolve; });
  const barrier = new Promise<void>(resolve => { release = resolve; });
  const listDevices = vi.fn(async () => { entered(); await barrier; return { devices: [] }; });
  (r as any).api = { listDevices };
  const first = r.loadDevices(false);
  await started;
  const second = r.loadDevices(false);
  // Let the joining call observe the in-flight load before changing the head.
  const sync = r.security.sync();
  await sync;
  await r.world.fixture.remove([r.machine]);
  await r.security.sync();
  release();
  await Promise.all([first, second]);
  expect(listDevices).toHaveBeenCalledTimes(2);
  expect(r.devices.has(r.machine)).toBe(false);
  expect((r as any).directoryHead).toEqual(await r.security.localStateHash());
}, 20000);

it("does not reuse a directory while recovery publication or reconciliation is pending", async () => {
  const r = await probe(), api = countedDirectory(r);
  await r.loadDevices(false);
  (r as any).recoveryObjectsPending = true;
  await r.loadDevices(false, { machineId: r.machine });
  expect(api.listDevices).toHaveBeenCalledTimes(2);
  (r as any).reconcilingSecurity = 1;
  await r.loadDevices(false, { machineId: r.machine });
  expect(api.listDevices).toHaveBeenCalledTimes(3);
  (r as any).reconcilingSecurity = 0;
  vi.spyOn(r.authority, "pending").mockResolvedValue({} as never);
  await r.loadDevices(false, { machineId: r.machine });
  expect(api.listDevices).toHaveBeenCalledTimes(4);
}, 20000);

it("retries a coalesced directory invalidated by a device metadata push", async () => {
  const r = await probe(), self = r.member.account.deviceId;
  const seal = (seq: bigint, name: string) => r.member.sdk.seal({ kind: 13, purpose: Purpose.DeviceMeta,
    machine: "", session: "", request: self, seq, deadline: 0n, compression: 0 },
    toBinary(DeviceMetaSchema, create(DeviceMetaSchema, { name, platform: "web" })));
  let meta = await seal(1n, "before");
  const next = await seal(2n, "after");
  let entered!: () => void, release!: () => void;
  const started = new Promise<void>(resolve => { entered = resolve; });
  const barrier = new Promise<void>(resolve => { release = resolve; });
  const listDevices = vi.fn(async () => {
    const snapshot = meta;
    entered(); await barrier;
    return { devices: [create(DeviceSchema, { id: self, meta: snapshot })] };
  });
  (r as any).api = { listDevices };
  const first = r.loadDevices(false);
  await started;
  meta = next;
  const push = (r as any).onFrame({ case: "deviceUpdated", value: { device: create(DeviceSchema, { id: self }) } });
  await r.security.sync();
  release();
  await Promise.all([first, push]);
  expect(listDevices).toHaveBeenCalledTimes(2);
  expect(r.devices.get(self)?.meta?.name).toBe("after");
}, 20000);

it("reloads unknown envelope senders and still rejects their payloads", async () => {
  const r = await probe(), api = countedDirectory(r);
  await r.loadDevices(false);
  const row = await r.sessionRow("s");
  const { headerBytes, parseHeader } = await import("./device-security");
  const meta = { ...row.meta, securityHeader: headerBytes({
    ...parseHeader(new Uint8Array(row.meta.securityHeader)), sender: "f".repeat(32),
  }) };
  await (r as any).onFrame({ case: "sessionUpdated", value: { session: { ...row, meta } } });
  expect(api.listDevices).toHaveBeenCalledTimes(2);
  expect(r.sessions.get("s")?.info).toBeUndefined();
}, 20000);

it("continues verifying payload signatures when the directory is reused", async () => {
  const r = await probe(), api = countedDirectory(r);
  await r.loadDevices(false);
  const row = await r.sessionRow("s");
  row.meta.signature[0] ^= 1;
  await (r as any).onFrame({ case: "sessionUpdated", value: { session: row } });
  expect(api.listDevices).toHaveBeenCalledTimes(1);
  expect(r.sessions.get("s")?.info).toBeUndefined();
}, 20000);

it("shares one full directory listing across concurrent refreshes at the same head", async () => {
  const r = await probe();
  let entered!: () => void, release!: () => void;
  const started = new Promise<void>(resolve => { entered = resolve; });
  const barrier = new Promise<void>(resolve => { release = resolve; });
  const listDevices = vi.fn(async () => { entered(); await barrier; return { devices: [] }; });
  (r as any).api = { listDevices };
  const first = r.loadDevices(false);
  await started;
  const second = r.loadDevices(false), third = r.loadDevices(false);
  await r.security.sync();
  release();
  await Promise.all([first, second, third]);
  expect(listDevices).toHaveBeenCalledTimes(1);
  expect(r.devicesStatus).toBe("ready");
}, 20000);

it("describes a newly paired browser without metadata and leaves directory updates usable", async () => {
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
  expect(putDeviceMeta).toHaveBeenCalledTimes(1);
  expect(r.devices.get(self)?.meta?.platform).toBe("web");
  expect(r.devices.get(self)?.metaRev).toBe(1n);
  expect((r as any).directoryJob).toBeUndefined();
  expect((r as any).descriptionPending).toBe(false);
  await r.loadDevices();
  const row = await r.sessionRow("s");
  await (r as any).onFrame({ case: "sessionUpdated", value: { session: row } });
  expect(r.sessions.get("s")?.info?.title).toBe("synthetic session");
  expect(putDeviceMeta).toHaveBeenCalledTimes(1);
  expect((r as any).directoryJob).toBeUndefined();
}, 10000);

it("clears shared directory work before self-description for both the owner and joined refreshes", async () => {
  const r = await probe();
  let entered!: () => void, release!: () => void;
  const started = new Promise<void>(resolve => { entered = resolve; });
  const barrier = new Promise<void>(resolve => { release = resolve; });
  const listDevices = vi.fn(async () => { entered(); await barrier; return { devices: [] }; });
  (r as any).api = { listDevices };
  const jobs: unknown[] = [];
  const describe = vi.spyOn(r as any, "describeSelf").mockImplementation(async () => {
    jobs.push((r as any).directoryJob);
  });
  const first = r.loadDevices();
  await started;
  const second = r.loadDevices();
  await r.security.sync();
  release();
  await Promise.all([first, second]);
  expect(listDevices).toHaveBeenCalledTimes(1);
  expect(describe).toHaveBeenCalledTimes(2);
  expect(jobs).toEqual([undefined, undefined]);
});
