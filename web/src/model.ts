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
