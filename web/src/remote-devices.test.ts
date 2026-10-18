// Trusted catalog races against a malicious relay's mutable directory hints.
import { create, toBinary } from "@bufbuild/protobuf";
import { afterAll, afterEach, expect, it, vi } from "vitest";
import { Remote } from "./remote";
import { DeviceMetaSchema } from "./gen/remoteagent/v1/remote_pb";
import { DeviceKind, DeviceSchema } from "./gen/remoteagent/v1/relay_pb";
import {
  Purpose,
  parseCapsule,
  capsuleBytes,
  parseHeader,
  commitment,
  fromEnvelope,
} from "./device-security";
import {
  GoSecurityFixture,
  webMember,
  removeFixtureBuild,
} from "./test/security-fixture";

const close: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const cleanup of close.splice(0)) await cleanup();
  vi.restoreAllMocks();
});
afterAll(removeFixtureBuild);
async function setup() {
  const f = await GoSecurityFixture.create();
  const member = await webMember(f);
  const other = await f.fixture.enroll(f.machine);
  const r = new (class extends Remote {
    constructor() {
      super(member.account, f.legacyKeys, undefined, member.storage);
    }
  })();
  let hints = [member.account.deviceId, other.record.memberID];
  const api = {
    listDevices: async () => ({
      devices: hints.map((id) =>
        create(DeviceSchema, { id, kind: DeviceKind.CLIENT }),
      ),
    }),
    listMachines: async () => ({ machines: [] }),
    forward: async (req: { securityRpc: Uint8Array }) => ({
      securityRpc: await f.fixture.rpc(
        capsuleBytes(parseCapsule(new Uint8Array(req.securityRpc))),
      ),
    }),
    removeDevice: async () => ({}),
  };
  (r as unknown as { api: object }).api = api;
  r.conn = "online";
  close.push(async () => {
    r.stop();
    await f.fixture.close();
  });
  const named = async (rev: bigint, name: string) => {
    await member.sdk.sync();
    return member.sdk.seal(
      {
        kind: 13,
        purpose: Purpose.DeviceMeta,
        machine: "",
        session: "",
        request: other.record.memberID,
        seq: rev,
        deadline: 0n,
        compression: 0,
      },
      toBinary(DeviceMetaSchema, create(DeviceMetaSchema, { name })),
    );
  };
  const remove = async (env: Awaited<ReturnType<typeof named>>) => {
    await member.sdk.sync();
    const hash = await member.sdk.localStateHash();
    return member.sdk.remove(
      other.record.memberID,
      await commitment(fromEnvelope(env)),
      hash,
    );
  };
  return {
    f,
    r,
    member,
    other,
    api,
    named,
    remove,
    setHints: (ids: string[]) => {
      hints = ids;
    },
  };
}

it("keeps the newer authenticated name when an older catalog finishes opening later", async () => {
  const { r, named, other } = await setup();
  await named(1n, "old name");
  const original = r.security.openHistory.bind(r.security);
  let release!: () => void;
  let started!: () => void;
  const opening = new Promise<void>((resolve) => {
    started = resolve;
  });
  vi.spyOn(r.security, "openHistory").mockImplementation(async (env, seed) => {
    const opened = await original(env, seed);
    if (
      parseHeader(new Uint8Array(env.securityHeader)).purpose ===
        Purpose.DeviceMeta &&
      opened.header.seq === 1n
    ) {
      started();
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    }
    return opened;
  });
  const old = r.loadDevices(false);
  await opening;
  await named(2n, "new name");
  await r.loadDevices(false);
  release();
  await old;
  expect(r.devices.get(other.record.memberID)?.meta?.name).toBe("new name");
  expect(r.devices.get(other.record.memberID)?.metaRev).toBe(2n);
}, 20000);

it("does not restore a device excluded from the signed roster when its old metadata finishes opening", async () => {
  const { r, named, other, remove } = await setup();
  const envelope = await named(1n, "old name");
  const original = r.security.openHistory.bind(r.security);
  let release!: () => void;
  let started!: () => void;
  let once = false;
  const opening = new Promise<void>((resolve) => {
    started = resolve;
  });
  vi.spyOn(r.security, "openHistory").mockImplementation(async (env, seed) => {
    const opened = await original(env, seed);
    if (!once && opened.header.purpose === Purpose.DeviceMeta) {
      once = true;
      started();
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    }
    return opened;
  });
  const old = r.loadDevices(false);
  await opening;
  await remove(envelope);
  await r.loadDevices(false);
  release();
  await old;
  expect(r.devices.has(other.record.memberID)).toBe(false);
}, 20000);

it("uses signed membership despite stale listings and forged device pushes", async () => {
  const { r, api, other, member } = await setup();
  await r.loadDevices(false);
  let answer!: (reply: {
    devices: ReturnType<typeof create<typeof DeviceSchema>>[];
  }) => void;
  let started!: () => void;
  let first = true;
  const queried = new Promise<void>((resolve) => {
    started = resolve;
  });
  const original = api.listDevices;
  api.listDevices = async () => {
    if (first) {
      first = false;
      started();
      return new Promise((resolve) => {
        answer = resolve;
      });
    }
    return original();
  };
  const stale = r.loadDevices(false);
  await queried;
  const rogue = "ffffffffffffffffffffffffffffffff";
  await (r as unknown as { onFrame(frame: unknown): Promise<void> }).onFrame({
    case: "deviceUpdated",
    value: {
      device: create(DeviceSchema, { id: rogue, kind: DeviceKind.CLIENT }),
    },
  });
  await r.loadDevices(false);
  answer({
    devices: [create(DeviceSchema, { id: rogue, kind: DeviceKind.CLIENT })],
  });
  await stale;
  expect(r.devices.has(rogue)).toBe(false);
  expect(r.devices.has(other.record.memberID)).toBe(true);
  expect(r.devices.has(member.account.deviceId)).toBe(true);
}, 20000);
