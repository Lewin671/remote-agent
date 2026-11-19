import { t, type Locale } from "./i18n";
import type { UsageBarBucketView, UsageStripPointView } from "./gen/remoteagent/core/v1/usage_view_pb";
import { UsageBucketGranularity, UsageRangePreset } from "./gen/remoteagent/v1/local_pb";

export interface StripPaths {
  d: string;
  dots: Array<{ x: number; y: number }>;
  coords: Array<{ x: number; y: number } | null>;
}

export function buildStripPaths(
  points: readonly UsageStripPointView[],
  scaleMin: number,
  scaleMax: number,
  width: number,
  height: number,
): StripPaths {
  const n = points.length;
  if (n === 0 || width <= 0 || height <= 0) {
    return { d: "", dots: [], coords: [] };
  }
  const span = Math.max(scaleMax - scaleMin, 1e-9);
  const innerH = Math.max(1, height - 6);
  const coords = points.map((pt, i) => {
    if (!pt.hasValue) return null;
    const x = ((i + 0.5) / n) * width;
    const y = height - 3 - ((pt.value - scaleMin) / span) * innerH;
    return { x, y };
  });

  let d = "";
  const dots: Array<{ x: number; y: number }> = [];
  let pen = false;
  for (let i = 0; i < coords.length; i++) {
    const c = coords[i];
    if (!c) {
      pen = false;
      continue;
    }
    const prev = i > 0 ? coords[i - 1] : null;
    const next = i + 1 < coords.length ? coords[i + 1] : null;
    if (!prev && !next) {
      dots.push(c);
      pen = false;
      continue;
    }
    d += `${pen ? "L" : "M"}${c.x.toFixed(1)} ${c.y.toFixed(1)}`;
    pen = true;
  }
  return { d, dots, coords };
}

export interface UsageAxisTick {
  pct: number;
  label: string;
  align: "s" | "" | "e";
}

function fmtMonthDay(dt: Date): string {
  return [dt.getMonth() + 1, dt.getDate()].join("/");
}

function effectiveGranularity(granularity: UsageBucketGranularity, preset: UsageRangePreset): UsageBucketGranularity {
  if (granularity !== UsageBucketGranularity.UNSPECIFIED) return granularity;
  if (preset === UsageRangePreset.TODAY) return UsageBucketGranularity.HOUR;
  if (preset === UsageRangePreset.SEVEN_DAYS) return UsageBucketGranularity.SIX_HOURS;
  return UsageBucketGranularity.DAY;
}

export function buildUsageAxisTicks(
  granularity: UsageBucketGranularity,
  buckets: readonly UsageBarBucketView[],
  startMs: bigint,
  endMs: bigint,
  preset: UsageRangePreset,
  locale: Locale,
): UsageAxisTick[] {
  const gran = effectiveGranularity(granularity, preset);
  if (gran === UsageBucketGranularity.HOUR) {
    return [
      { pct: 0, label: "0", align: "s" },
      { pct: 25, label: "6", align: "" },
      { pct: 50, label: "12", align: "" },
      { pct: 75, label: "18", align: "" },
      { pct: 100, label: t("usage.axis_24h", undefined, locale), align: "e" },
    ];
  }

  const now = new Date();
  const todayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const fallbackDays = preset === UsageRangePreset.THIRTY_DAYS ? 30 : 7;
  const effStartMs =
    startMs > 0n
      ? Number(startMs)
      : buckets.length > 0 && buckets[0].startMs > 0n
        ? Number(buckets[0].startMs)
        : todayStart - (fallbackDays - 1) * 86_400_000;

  if (gran === UsageBucketGranularity.SIX_HOURS) {
    const n = buckets.length > 0 ? buckets.length : 28;
    const days = Math.max(1, Math.round(n / 4));
    const ticks: UsageAxisTick[] = [];
    let prevMonth = -1;
    for (let d = 0; d < days; d++) {
      const idx = d * 4;
      const ms = idx < buckets.length && buckets[idx].startMs > 0n ? Number(buckets[idx].startMs) : effStartMs + d * 86_400_000;
      const dt = new Date(ms);
      const m = dt.getMonth() + 1;
      const day = dt.getDate();
      const tickLabel = d === 0 || m !== prevMonth ? fmtMonthDay(dt) : String(day);
      prevMonth = m;
      ticks.push({
        pct: ((d * 4 + 2) / n) * 100,
        label: tickLabel,
        align: "",
      });
    }
    return ticks;
  }

  const derivedDays =
    endMs > startMs && startMs > 0n
      ? Math.max(1, Math.round((Number(endMs) - Number(startMs)) / 86_400_000))
      : fallbackDays;
  const n = buckets.length > 0 ? buckets.length : derivedDays;
  if (n <= 1) {
    const dt = new Date(effStartMs);
    return [{ pct: 50, label: fmtMonthDay(dt), align: "" }];
  }

  const lastMs =
    buckets.length > 0 && buckets[buckets.length - 1].startMs > 0n
      ? Number(buckets[buckets.length - 1].startMs)
      : endMs > 0n
        ? Number(endMs) - 1
        : effStartMs + (n - 1) * 86_400_000;

  const tickCount = n <= 4 ? n : n <= 18 ? 4 : 5;
  const ticks: UsageAxisTick[] = [];
  for (let k = 0; k < tickCount; k++) {
    const last = k === tickCount - 1;
    const posIdx = last ? n : Math.round((k * n) / (tickCount - 1));
    let ms: number;
    if (last) {
      ms = lastMs;
    } else if (posIdx < buckets.length && buckets[posIdx].startMs > 0n) {
      ms = Number(buckets[posIdx].startMs);
    } else {
      ms = effStartMs + posIdx * 86_400_000;
    }
    const dt = new Date(ms);
    ticks.push({
      pct: (posIdx / n) * 100,
      label: fmtMonthDay(dt),
      align: k === 0 ? "s" : last ? "e" : "",
    });
  }
  return ticks;
}

export function formatUsageBucketLabel(
  startMs: bigint,
  endMs: bigint,
  granularity: UsageBucketGranularity,
  locale: Locale,
): string {
  const s = new Date(Number(startMs));
  const e = new Date(Number(endMs > startMs ? endMs : startMs + 3_600_000n));
  const startH = s.getHours();
  let endH = e.getHours();
  if (endH <= startH && endMs > startMs) endH = 24;
  if (granularity === UsageBucketGranularity.HOUR) {
    return t("usage.scrub_hour_range", { start: startH, end: endH }, locale);
  }
  if (granularity === UsageBucketGranularity.SIX_HOURS) {
    return t(
      "usage.scrub_day_hour_range",
      { month: s.getMonth() + 1, day: s.getDate(), start: startH, end: endH },
      locale,
    );
  }
  return t("usage.scrub_day", { month: s.getMonth() + 1, day: s.getDate() }, locale);
}

export function formatUsageCustomRange(startMs: bigint, inclusiveEndMs: bigint, locale: Locale): string {
  const s = new Date(Number(startMs));
  const e = new Date(Number(inclusiveEndMs));
  const sy = s.getFullYear();
  const sm = s.getMonth() + 1;
  const sd = s.getDate();
  const ey = e.getFullYear();
  const em = e.getMonth() + 1;
  const ed = e.getDate();
  if (sy === ey && sm === em && sd === ed) {
    return t("usage.custom_range_single_day", { year: sy, month: sm, day: sd }, locale);
  }
  if (sy === ey) {
    return t(
      "usage.custom_range_same_year",
      { year: sy, startMonth: sm, startDay: sd, endMonth: em, endDay: ed },
      locale,
    );
  }
  return t(
    "usage.custom_range_cross_year",
    { startYear: sy, startMonth: sm, startDay: sd, endYear: ey, endMonth: em, endDay: ed },
    locale,
  );
}
