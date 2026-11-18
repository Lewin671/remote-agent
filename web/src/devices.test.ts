import { t as message } from "./i18n";
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { clientName, defaultClientName, describeBrowser, platformName, type DeviceInfo } from "./devices";
import { DeviceKind } from "./gen/remoteagent/v1/relay_pb";
import { DeviceMetaSchema } from "./gen/remoteagent/v1/remote_pb";

const dev = (id: string, meta?: { name?: string; platform?: string; description?: string }): DeviceInfo => ({
  id,
  kind: DeviceKind.CLIENT,
  createdMs: 0n,
  metaRev: meta ? 1n : 0n,
  meta: meta && create(DeviceMetaSchema, meta),
  lastSeenMs: 0n,
});
const ctx = {
  devices: new Map([
    ["bbbb0000bbbb0000bbbb0000bbbbb71c", dev("bbbb0000bbbb0000bbbb0000bbbbb71c", { platform: "android", description: "Android 16" })],
    ["cccc0000cccc0000cccc0000cccc92de", dev("cccc0000cccc0000cccc0000cccc92de")],
    ["dddd0000dddd0000dddd0000ddddpxl1", dev("dddd0000dddd0000dddd0000ddddpxl1", { name: "我的手机", platform: "android" })],
  ]),
};

describe("device names", () => {
  it("uses the given name, else what the client runs on", () => {
    const all = ctx.devices;
    expect(clientName(all.get("dddd0000dddd0000dddd0000ddddpxl1"), all)).toBe("我的手机");
    expect(clientName(all.get("bbbb0000bbbb0000bbbb0000bbbbb71c"), all)).toBe("Android · b71c");
    expect(clientName(all.get("cccc0000cccc0000cccc0000cccc92de"), all)).toBe(message("security.client"));
    expect(platformName(create(DeviceMetaSchema, { platform: "web", description: "Chrome on macOS" }))).toBe("Chrome · macOS");
    expect(platformName(create(DeviceMetaSchema, { platform: "web", description: "Firefox" }))).toBe("Firefox");
    expect(platformName(create(DeviceMetaSchema, { platform: "web" }))).toBe(message("security.browser"));
    expect(platformName(create(DeviceMetaSchema, { platform: "android", description: "Android 16 · Pixel 9" }))).toBe("Android · Pixel 9");
    expect(platformName(create(DeviceMetaSchema, { platform: "ios", description: "iOS 26.0 · iPad" }))).toBe("iPad");
    expect(platformName(create(DeviceMetaSchema, { platform: "ios" }))).toBe("iOS");
    expect(platformName(undefined)).toBe(message("security.client"));
    expect(defaultClientName(dev("dddd0000dddd0000dddd0000ddddpxl1", { name: "我的手机", platform: "android", description: "Android 16 · Pixel 9" }), all)).toBe("Android · Pixel 9");
  });

  it("distinguishes two clients on the same OS", () => {
    const a = dev("aaaa0000aaaa0000aaaa0000aaaa1111", { platform: "web", description: "Chrome on macOS" });
    const b = dev("bbbb0000bbbb0000bbbb0000bbbb2222", { platform: "web", description: "Chrome on macOS" });
    const c = dev("cccc0000cccc0000cccc0000cccc3333", { platform: "web", description: "Safari on iOS" });
    const m = { ...dev("dddd0000dddd0000dddd0000dddd4444", { platform: "web", description: "Chrome on macOS" }), kind: DeviceKind.MACHINE };
    const all = new Map([a, b, c, m].map((d) => [d.id, d]));
    expect(clientName(a, all)).toBe("Chrome · macOS · 1111");
    expect(clientName(b, all)).toBe("Chrome · macOS · 2222");
    expect(clientName(c, all)).toBe("Safari · iOS");
    all.delete(b.id);
    expect(clientName(a, all)).toBe("Chrome · macOS");
  });

  it("describes this browser without its version", () => {
    expect(describeBrowser("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36")).toBe("Chrome on macOS");
    expect(describeBrowser("Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1")).toBe("Safari on iOS");
  });
});
