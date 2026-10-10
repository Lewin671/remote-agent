// Device metadata arriving out of order (review B10).
import { create } from "@bufbuild/protobuf";
import { expect, it, vi } from "vitest";
import { PayloadKind } from "./gen/remoteagent/v1/envelope_pb";

// The older revision takes longer to open, as it may in a browser.
vi.mock("./crypto", async (orig) => {
  const real = await orig<typeof import("./crypto")>();
  return {
    ...real,
    openMessage: async (...args: Parameters<typeof real.openMessage>) => {
      const r = await real.openMessage(...args);
      if (r.header.kind === real.PayloadKind.DEVICE_META && r.header.seq === 1n) await new Promise((res) => setTimeout(res, 30));
      return r;
    },
  };
});

const { Remote } = await import("./remote");
const { sealMessage } = await import("./crypto");
const { DeviceMetaSchema } = await import("./gen/remoteagent/v1/remote_pb");
const { DeviceKind, DeviceSchema } = await import("./gen/remoteagent/v1/relay_pb");

async function setup() {
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  class Probe extends Remote {
    constructor() {
      super({ server: "https://ra.example.com", root: key, rootRaw: new Uint8Array(32), devicePrivate: device.privateKey, deviceId: "me" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key });
    }
    put(d: unknown) {
      return (this as unknown as { putDevice(d: unknown): Promise<void> }).putDevice(d);
    }
  }
  const r = new Probe();
  const dev = async (rev: bigint, name: string) =>
    create(DeviceSchema, {
      id: "other",
      kind: DeviceKind.CLIENT,
      metaRevision: rev,
      meta: await sealMessage(key, { kind: PayloadKind.DEVICE_META, requestId: "other", seq: rev }, DeviceMetaSchema, create(DeviceMetaSchema, { name })),
    });
  return { r, dev };
}

it("keeps the newer name when an older revision finishes opening later", async () => {
  const { r, dev } = await setup();
  const [one, two] = [await dev(1n, "旧名字"), await dev(2n, "新名字")];
  await Promise.all([r.put(one), r.put(two)]);
  expect(r.devices.get("other")?.meta?.name).toBe("新名字");
  expect(r.devices.get("other")?.metaRev).toBe(2n);
});

it("does not bring back a device removed while its metadata was opening", async () => {
  const { r, dev } = await setup();
  const p = r.put(await dev(1n, "旧名字"));
  // Removed meanwhile, as a deviceRemoved push would.
  (r as unknown as { onFrame(k: unknown): Promise<void> }).onFrame({ case: "deviceRemoved", value: { deviceId: "other" } });
  await p;
  expect(r.devices.has("other")).toBe(false);
});

it("leaves a device as a push left it while the list was on its way", async () => {
  const { r, dev } = await setup();
  await r.put(await dev(2n, "旧设备"));
  // The server has answered with the list as it was; the answer is still on its way.
  let answer!: (list: unknown) => void;
  (r as unknown as { api: object }).api = { listDevices: () => new Promise((res) => (answer = res)) };
  const reading = r.loadDevices();
  const frame = (k: unknown) => (r as unknown as { onFrame(k: unknown): Promise<void> }).onFrame(k);
  await frame({ case: "deviceRemoved", value: { deviceId: "other" } });
  await frame({ case: "deviceUpdated", value: { device: create(DeviceSchema, { id: "new", kind: DeviceKind.CLIENT }) } });
  answer({ devices: [await dev(2n, "旧设备")] });
  await reading;
  expect(r.devices.has("other")).toBe(false);
  expect(r.devices.has("new")).toBe(true);
});
