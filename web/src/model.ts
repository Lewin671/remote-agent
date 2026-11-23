import * as l10n from "./i18n";
import { t } from "./i18n";
import type { MachineMeta } from "./gen/remoteagent/v1/remote_pb";

export type Tone = "run" | "wait" | "idle" | "off";

export function machineName(m?: { id: string; meta?: MachineMeta }): string {
  return (
    m?.meta?.hostname?.replace(/\.local$/, "") ||
    (m ? m.id.slice(0, 8) : t("common.unknown_computer"))
  );
}

export function hhmm(ms: number): string {
  const d = new Date(ms);
  return `${d.getHours()}:${String(d.getMinutes()).padStart(2, "0")}`;
}

export function clock(ms: number): string {
  const s = Math.floor(ms / 1000);
  const m = Math.floor(s / 60);
  const h = Math.floor(m / 60);
  return h > 0
    ? `${h}:${String(m % 60).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`
    : `${m}:${String(s % 60).padStart(2, "0")}`;
}

/** A future local time, independent of the browser's locale.
 * Compare calendar dates so midnight, month/year changes and DST stay correct.
 * Pairing expiry uses only the zero-padded 24-hour clock. */
export function futureTime(ms: number | bigint, now = Date.now(), format: "date-time" | "time" = "date-time"): string {
  const time = Number(ms);
  const clockStr = hhmm(time).padStart(5, "0");
  if (format === "time") return clockStr;
  const date = new Date(time);
  const today = new Date(now);
  const tomorrow = new Date(now);
  tomorrow.setDate(tomorrow.getDate() + 1);
  const label = date.toDateString() === today.toDateString() ? l10n.t("time.today")
    : date.toDateString() === tomorrow.toDateString() ? l10n.t("time.tomorrow")
    : l10n.t("time.month_day", { month: date.getMonth() + 1, day: date.getDate() });
  return `${label} ${clockStr}`;
}

/** Home folders as ~. */
export function shortPath(p: string): string {
  return p.replace(/^\/Users\/[^/]+/, "~").replace(/^\/home\/[^/]+/, "~");
}

/** macOS reaches /tmp, /var and /etc through /private; either spelling
 * names the same folder. */
function samePlace(p: string): string {
  return p.replace(/^\/private(?=\/(?:tmp|var|etc)(?:\/|$))/, "");
}

/** One whole path shortened by the same rules, spaces and all, as a file
 * edit's title is; unchanged if it is not under the session or home
 * folder. */
export function shortenPath(path: string, cwd: string, home = ""): string {
  const base = samePlace(cwd).replace(/\/+$/, "");
  const h = samePlace(home).replace(/\/+$/, "");
  const under = (p: string, dir: string): string | undefined => {
    if (!dir || !p.startsWith(dir)) return undefined;
    const rest = p.slice(dir.length);
    return rest === "" || rest[0] === "/" || /^:\d+(:\d+)*$/.test(rest)
      ? rest
      : undefined;
  };
  const p = samePlace(path);
  const inCwd = under(p, base);
  if (inCwd !== undefined)
    return inCwd === "" || inCwd === "/"
      ? "."
      : inCwd[0] === "/"
        ? inCwd.slice(1)
        : "." + inCwd;
  const hm = h || /^\/(?:Users|home)\/[^/:]+/.exec(p)?.[0] || "";
  const inHome = under(p, hm);
  return inHome === undefined ? path : "~" + inHome;
}
