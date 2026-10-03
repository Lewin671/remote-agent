// The account's devices as clients name them, and where an action came
// from (architecture §9 多客户端的界面状态). A device is named only when
// the daemon verified its signature; anything unsure is "another client"
// or "the local terminal", never a guess.

import type { Origin } from "./gen/remoteagent/v1/event_pb";
import { OriginKind } from "./gen/remoteagent/v1/event_pb";
import { DeviceKind } from "./gen/remoteagent/v1/relay_pb";
import type { DeviceMeta } from "./gen/remoteagent/v1/remote_pb";

export interface DeviceInfo {
  id: string;
  kind: DeviceKind;
  createdMs: bigint;
  meta?: DeviceMeta;
  metaRev: bigint;
  // When it was last used, 0 without a record.
  lastSeenMs: bigint;
}

/** The last four characters of a device ID, as lists show it. */
export function shortId(id: string): string {
  return id.slice(-4);
}

/** What kind of client, from what it says it runs on: Chrome, Safari,
 * Android; a browser that says nothing is 浏览器. */
export function platformLabel(meta?: Pick<DeviceMeta, "platform" | "description">): string {
  const first = meta?.description.trim().split(/\s+/)[0] ?? "";
  if (first && !/^\d/.test(first)) return first;
  if (meta?.platform === "android") return "Android";
  if (meta?.platform === "web") return "浏览器";
  return "客户端";
}

/** A client's name: the one the user gave it, else its kind and short ID
 * (Android · b71c). */
export function clientName(d: Pick<DeviceInfo, "id" | "meta"> | undefined, id = d?.id ?? ""): string {
  return d?.meta?.name.trim() || defaultClientName(d, id);
}

/** The name a client has until the user gives it one, and again once they
 * take theirs back. */
export function defaultClientName(d: Pick<DeviceInfo, "id" | "meta"> | undefined, id = d?.id ?? ""): string {
  return `${platformLabel(d?.meta)} · ${shortId(id)}`;
}

/** What a client is, under its name in the list: an app or a browser, and
 * what it says it runs on. */
export function clientDetail(d: Partial<Pick<DeviceInfo, "meta">> | undefined): string {
  const kind = d?.meta?.platform === "android" ? "App" : d?.meta?.platform === "web" ? "浏览器" : "";
  return [kind, d?.meta?.description.trim()].filter(Boolean).join(" · ") || "种类未知";
}

/** A day as lists write it: 9月27日, with the year when it is not this one. */
export function dayText(ms: number | bigint, now = Date.now()): string {
  const day = new Date(Number(ms));
  return `${day.getFullYear() === new Date(now).getFullYear() ? "" : `${day.getFullYear()}年`}${day.getMonth() + 1}月${day.getDate()}日`;
}

/** When a client was last used: 刚刚, 3 分钟前, 今天 9:12, 昨天, 9月27日;
 * "" when the server has no record of it. */
export function lastUsed(d: Partial<Pick<DeviceInfo, "lastSeenMs">> | undefined, now = Date.now()): string {
  const t = Number(d?.lastSeenMs ?? 0);
  if (!t) return "";
  const ago = now - t;
  if (ago < 60_000) return "刚刚";
  if (ago < 3_600_000) return `${Math.floor(ago / 60_000)} 分钟前`;
  const then = new Date(t);
  if (then.toDateString() === new Date(now).toDateString()) return `今天 ${then.getHours()}:${String(then.getMinutes()).padStart(2, "0")}`;
  if (then.toDateString() === new Date(now - 86_400_000).toDateString()) return "昨天";
  return dayText(t, now);
}

/** What the list says beside a client: when it was last used, else the
 * day it was added. */
export function clientUsed(d: Partial<Pick<DeviceInfo, "lastSeenMs" | "createdMs">> | undefined, now = Date.now()): string {
  const used = lastUsed(d, now);
  if (used) return `${used}${/\d$/.test(used) ? " " : ""}使用`;
  return Number(d?.createdMs ?? 0) ? `${dayText(d!.createdMs!, now)}添加` : "已配对";
}

/** Clients as the list orders them: this one, then the most recently used
 * first, then those with no record, the newest first. */
export function sortClients<T extends Pick<DeviceInfo, "id" | "createdMs" | "lastSeenMs">>(clients: T[], me: string): T[] {
  return [...clients].sort((a, b) => Number(b.id === me) - Number(a.id === me) || Number(b.lastSeenMs - a.lastSeenMs) || Number(b.createdMs - a.createdMs));
}

/** What this browser says about itself in its device metadata. */
export function describeBrowser(ua: string): string {
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "浏览器";
  const os = /iPhone|iPad/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Mac OS X/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "";
  return os ? `${browser} on ${os}` : browser;
}

export interface OriginContext {
  me: string; // this device's ID
  devices: Map<string, DeviceInfo>;
  machine: string; // the session's computer, by name
}

/** Who did something, in words; "" for the CLI or daemon itself, and
 * undefined when the daemon recorded no origin (a message it could not
 * tell the sender of), for the caller's own fallback. */
export function originLabel(o: Origin | undefined, ctx: OriginContext): string | undefined {
  if (!o || o.kind === OriginKind.UNSPECIFIED) return undefined;
  switch (o.kind) {
    case OriginKind.CLIENT: {
      if (o.deviceId === ctx.me) return "本客户端";
      const d = ctx.devices.get(o.deviceId);
      // A device removed since keeps only its short ID.
      return d ? clientName(d) : `客户端 · ${shortId(o.deviceId)}`;
    }
    case OriginKind.LOCAL:
      return `${ctx.machine} 本地终端`;
    case OriginKind.SYSTEM:
      return "";
  }
  return undefined;
}

/** Who settled something shown on this page as waiting (an approval, a
 * question, a plan) when it was not this client: "另一端" unless the
 * daemon named a device, "终端" for the computer. */
export function elsewhereLabel(o: Origin | undefined, ctx: OriginContext): string {
  const l = originLabel(o, ctx);
  if (l === undefined) return "另一端";
  if (o?.kind === OriginKind.LOCAL) return "终端";
  return l;
}
