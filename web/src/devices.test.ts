import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { clientDetail, clientName, clientUsed, defaultClientName, sortClients, describeBrowser, elsewhereLabel, originLabel, platformLabel, type DeviceInfo } from "./devices";
import { OriginKind, OriginSchema } from "./gen/remoteagent/v1/event_pb";
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
const origin = (kind: OriginKind, deviceId = "", requestId = "") => create(OriginSchema, { kind, deviceId, requestId });
const ctx = {
  me: "aaaa0000aaaa0000aaaa0000aaaaa8f2",
  machine: "VPS-dev",
  devices: new Map([
    ["bbbb0000bbbb0000bbbb0000bbbbb71c", dev("bbbb0000bbbb0000bbbb0000bbbbb71c", { platform: "android", description: "Android 16" })],
    ["cccc0000cccc0000cccc0000cccc92de", dev("cccc0000cccc0000cccc0000cccc92de")],
    ["dddd0000dddd0000dddd0000ddddpxl1", dev("dddd0000dddd0000dddd0000ddddpxl1", { name: "我的手机", platform: "android" })],
  ]),
};

describe("device names", () => {
  it("uses the given name, else the kind of client and a short ID", () => {
    expect(clientName(ctx.devices.get("dddd0000dddd0000dddd0000ddddpxl1"))).toBe("我的手机");
    expect(clientName(ctx.devices.get("bbbb0000bbbb0000bbbb0000bbbbb71c"))).toBe("Android · b71c");
    expect(clientName(ctx.devices.get("cccc0000cccc0000cccc0000cccc92de"))).toBe("客户端 · 92de");
    expect(platformLabel(create(DeviceMetaSchema, { platform: "web", description: "Chrome on macOS" }))).toBe("Chrome");
    expect(platformLabel(create(DeviceMetaSchema, { platform: "web" }))).toBe("浏览器");
    expect(defaultClientName(dev("dddd0000dddd0000dddd0000ddddpxl1", { name: "我的手机", platform: "android", description: "Android 16 · Pixel 9" }))).toBe("Android · pxl1");
    const now = new Date(2026, 9, 3, 12).getTime();
    const at = (...d: [number, number, number, number?, number?]) => BigInt(new Date(...d).getTime());
    expect(clientDetail({ meta: create(DeviceMetaSchema, { platform: "web", description: "Chrome on macOS" }) })).toBe("浏览器 · Chrome on macOS");
    expect(clientDetail({ meta: create(DeviceMetaSchema, { platform: "android", description: "Android 16 · Pixel 9" }) })).toBe("App · Android 16 · Pixel 9");
    expect(clientDetail({ meta: create(DeviceMetaSchema, { platform: "ios", description: "iOS 27.0 · iPhone" }) })).toBe("App · iOS 27.0 · iPhone");
    expect(platformLabel(create(DeviceMetaSchema, { platform: "ios" }))).toBe("iOS");
    expect(clientDetail(undefined)).toBe("种类未知");
    expect(clientUsed({ lastSeenMs: BigInt(now - 30_000), createdMs: at(2026, 8, 1) }, now)).toBe("刚刚使用");
    expect(clientUsed({ lastSeenMs: BigInt(now - 5 * 60_000) }, now)).toBe("5 分钟前使用");
    expect(clientUsed({ lastSeenMs: at(2026, 9, 3, 9, 5) }, now)).toBe("今天 9:05 使用");
    expect(clientUsed({ lastSeenMs: at(2026, 9, 2, 23) }, now)).toBe("昨天使用");
    expect(clientUsed({ lastSeenMs: at(2026, 7, 3) }, now)).toBe("8月3日使用");
    expect(clientUsed({ lastSeenMs: 0n, createdMs: at(2025, 11, 1) }, now)).toBe("2025年12月1日添加");
    expect(clientUsed(undefined, now)).toBe("已配对");
    const order = sortClients(
      [
        { id: "old", createdMs: 1n, lastSeenMs: 0n },
        { id: "me", createdMs: 2n, lastSeenMs: 5n },
        { id: "new", createdMs: 3n, lastSeenMs: 0n },
        { id: "recent", createdMs: 1n, lastSeenMs: 9n },
        { id: "stale", createdMs: 1n, lastSeenMs: 4n },
      ],
      "me",
    );
    expect(order.map((d) => d.id)).toEqual(["me", "recent", "stale", "new", "old"]);
  });

  it("describes this browser without its version", () => {
    expect(describeBrowser("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36")).toBe("Chrome on macOS");
    expect(describeBrowser("Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1")).toBe("Safari on iOS");
  });
});

describe("where an action came from", () => {
  it("names the device that sent it", () => {
    expect(originLabel(origin(OriginKind.CLIENT, "bbbb0000bbbb0000bbbb0000bbbbb71c"), ctx)).toBe("Android · b71c");
    expect(originLabel(origin(OriginKind.CLIENT, ctx.me), ctx)).toBe("本客户端");
  });

  it("calls a removed device so, without its ID", () => {
    expect(originLabel(origin(OriginKind.CLIENT, "eeee0000eeee0000eeee0000eeee5a5a"), ctx)).toBe("已移除的设备");
  });

  it("says the computer's terminal, and nothing for the CLI itself", () => {
    expect(originLabel(origin(OriginKind.LOCAL), ctx)).toBe("VPS-dev 终端");
    expect(originLabel(origin(OriginKind.SYSTEM), ctx)).toBe("");
  });

  it("leaves the wording to the caller when the daemon recorded no origin", () => {
    expect(originLabel(undefined, ctx)).toBeUndefined();
    expect(originLabel(origin(OriginKind.UNSPECIFIED), ctx)).toBeUndefined();
  });

  it("falls back to 另一端 for something settled elsewhere", () => {
    expect(elsewhereLabel(undefined, ctx)).toBe("另一端");
    expect(elsewhereLabel(origin(OriginKind.LOCAL), ctx)).toBe("VPS-dev 终端");
    expect(elsewhereLabel(origin(OriginKind.CLIENT, "eeee0000eeee0000eeee0000eeee5a5a"), ctx)).toBe("已移除的设备");
    expect(elsewhereLabel(origin(OriginKind.CLIENT, "bbbb0000bbbb0000bbbb0000bbbbb71c"), ctx)).toBe("Android · b71c");
  });
});
