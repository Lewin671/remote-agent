import * as l10n from "./i18n";
// Device directory type and this browser's self-description for device metadata.
import type { DeviceKind } from "./gen/remoteagent/v1/relay_pb";
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

/** What this browser says about itself in its device metadata. */
export function describeBrowser(ua: string): string {
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : l10n.t("security.browser", {}, "zh-CN");
  const os = /iPhone|iPad/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Mac OS X/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "";
  return os ? `${browser} on ${os}` : browser;
}
