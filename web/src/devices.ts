import * as l10n from "./i18n";
// The account's devices as security views name them, and this browser's description.
import { DeviceKind } from "./gen/remoteagent/v1/relay_pb";
import type { DeviceMeta } from "./gen/remoteagent/v1/remote_pb";

export interface DeviceInfo {
  id: string;
  kind: DeviceKind;
  createdMs: bigint;
  meta?: DeviceMeta;
  metaRev: bigint;
  /** Signed member not yet present in the registered device directory. */
  pairingIncomplete?: boolean;
  // When it was last used, 0 without a record.
  lastSeenMs: bigint;
}

/** The last four characters of a device ID, as lists show it. */
export function shortId(id: string): string {
  return id.slice(-4);
}

/** What a client runs on, as its default name says it (design-system §8
 * 设备的名字): a browser and its system (Chrome · macOS), an Android
 * phone and its model (Android · Pixel 9), an iPhone or iPad. Read from
 * what each client puts in its metadata: "Chrome on macOS",
 * "Android 16 · Pixel 9", "iOS 26.0 · iPhone". */
export function platformName(meta?: Pick<DeviceMeta, "platform" | "description">): string {
  const desc = meta?.description.trim() ?? "";
  // What follows the system's version: the phone's model, or iPhone/iPad.
  const model = desc.split(" · ").slice(1).join(" · ").trim();
  switch (meta?.platform) {
    case "web": {
      const [browser, os] = desc.split(" on ").map((x) => x.trim());
      return browser ? [browser, os].filter(Boolean).join(" · ") : l10n.t("security.browser");
    }
    case "android":
      return model ? `Android · ${model}` : "Android";
    case "ios":
      return model || "iOS";
  }
  return l10n.t("security.client");
}

/** A client's name: the one the user gave it, else its default name. */
export function clientName(d: Pick<DeviceInfo, "id" | "meta"> | undefined, devices: ReadonlyMap<string, Pick<DeviceInfo, "id" | "kind" | "meta">>, id = d?.id ?? ""): string {
  return d?.meta?.name.trim() || defaultClientName(d, devices, id);
}

/** The name a client has until the user gives it one, and again once they
 * take theirs back: what it runs on, and its short ID only when another
 * client of the account would have the same one. */
export function defaultClientName(d: Pick<DeviceInfo, "id" | "meta"> | undefined, devices: ReadonlyMap<string, Pick<DeviceInfo, "id" | "kind" | "meta">>, id = d?.id ?? ""): string {
  const base = platformName(d?.meta);
  const twin = [...devices.values()].some((x) => x.id !== id && x.kind === DeviceKind.CLIENT && platformName(x.meta) === base);
  return twin ? `${base} · ${shortId(id)}` : base;
}

/** What this browser says about itself in its device metadata. */
export function describeBrowser(ua: string): string {
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : l10n.t("security.browser", {}, "zh-CN");
  const os = /iPhone|iPad/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Mac OS X/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "";
  return os ? `${browser} on ${os}` : browser;
}
