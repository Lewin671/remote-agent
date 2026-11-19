import type { Locale } from "./i18n";
import { UsageMetricTab as Metric } from "./gen/remoteagent/core/v1/usage_view_pb";
import type { UsageMetricTab, UsageSeriesView } from "./gen/remoteagent/core/v1/usage_view_pb";
import { UsageBucketGranularity, type UsageBucketPoint } from "./gen/remoteagent/v1/local_pb";
import { formatSpeedValue, formatTokens, formatTtftValue } from "./usageFormat";

export const MAX_OVERLAY_SERIES = 4;
export const DEFAULT_OVERLAY_SERIES = 3;
const FEW_SAMPLES_MIN = 3n;

export function bucketTokens(b: UsageBucketPoint): number {
  return Number(b.uncachedInputTokens + b.cacheReadTokens + b.cacheWriteTokens + b.outputTokens);
}

export function seriesTotalTokens(s: UsageSeriesView): number {
  const c = s.current;
  if (!c) return 0;
  return Number(c.uncachedInputTokens + c.cacheReadTokens + c.cacheWriteTokens + c.outputTokens);
}

export function bucketCacheHit(b: UsageBucketPoint): number | null {
  const denom = Number(b.uncachedInputTokens + b.cacheReadTokens + b.cacheWriteTokens);
  if (denom <= 0) return null;
  return Number(b.cacheReadTokens) / denom;
}

export function bucketHasSpeed(b: UsageBucketPoint): boolean {
  return b.speedRequestCount > 0n && b.speedP50 > 0;
}

export function bucketHasTtft(b: UsageBucketPoint): boolean {
  return b.speedRequestCount > 0n && b.ttftP50Ms > 0;
}

export function seriesIsFewSamples(s: UsageSeriesView, metric: UsageMetricTab): boolean {
  if (metric === Metric.TOKENS) return false;
  if (metric === Metric.CACHE_HIT) {
    const n = s.current?.usageRequestCount ?? 0n;
    return n > 0n && n < FEW_SAMPLES_MIN;
  }
  const n = s.current?.speedRequestCount ?? 0n;
  return s.fewSpeedSamples || (n > 0n && n < FEW_SAMPLES_MIN);
}

export function formatDateLabel(ms: bigint, gran: UsageBucketGranularity): string {
  const d = new Date(Number(ms));
  if (gran === UsageBucketGranularity.HOUR) {
    return `${String(d.getHours()).padStart(2, "0")}:00`;
  }
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

export type ChartTick = { y: number; label: string };
export type BarSegment = { slot: number; y: number; h: number; key: string; val: number };
export type BarColumn = { bi: number; cx: number; bx: number; barW: number; total: number; segments: BarSegment[] };
export type LinePt = { bi: number; x: number; y: number; lowY?: number; highY?: number; val: number };
export type LineSeriesGeom = {
  series: UsageSeriesView;
  slot: number;
  few: boolean;
  linePaths: string[];
  bandPolygons: string[];
  points: LinePt[];
};

export type BuiltChart =
  | { kind: "bars"; xW: number; y0: number; yH: number; base: number; step: number; yTicks: ChartTick[]; bars: BarColumn[]; xLabels: string[] }
  | { kind: "lines"; xW: number; y0: number; yH: number; base: number; xAt: (bi: number) => number; yTicks: ChartTick[]; lines: LineSeriesGeom[]; xLabels: string[] };

export function buildUsageChart(
  selectedSeries: UsageSeriesView[],
  bucketCount: number,
  metric: UsageMetricTab,
  granularity: UsageBucketGranularity,
  locale: Locale,
): BuiltChart | null {
  const x0 = 0;
  const xW = 294;
  const y0 = 10;
  const yH = 128;
  const base = y0 + yH;

  if (bucketCount <= 0 || selectedSeries.length === 0) return null;

  const firstBuckets = selectedSeries[0].buckets;
  const xLabels =
    firstBuckets.length > 0
      ? [
          formatDateLabel(firstBuckets[0].startMs, granularity),
          formatDateLabel(firstBuckets[Math.floor((firstBuckets.length - 1) / 2)].startMs, granularity),
          formatDateLabel(firstBuckets[firstBuckets.length - 1].startMs, granularity),
        ]
      : [];

  if (metric === Metric.TOKENS) {
    const totals = Array.from({ length: bucketCount }, (_, bi) =>
      selectedSeries.reduce((acc, s) => acc + (s.buckets[bi] ? bucketTokens(s.buckets[bi]) : 0), 0),
    );
    const maxVal = Math.max(1, ...totals);
    const yMax = Math.ceil(maxVal * 1.12);
    const yTicks: ChartTick[] = [yMax, Math.round(yMax / 2), 0].map((v, idx) => ({
      y: idx === 0 ? y0 : idx === 1 ? y0 + yH / 2 : base,
      label: v === 0 ? "0" : formatTokens(v, locale),
    }));
    const step = xW / Math.max(1, bucketCount);
    const barW = Math.max(3, Math.min(18, step * 0.62));
    const bars: BarColumn[] = Array.from({ length: bucketCount }, (_, bi) => {
      const cx = x0 + (bi + 0.5) * step;
      const bx = cx - barW / 2;
      let curY = base;
      const segments: BarSegment[] = [];
      for (let si = 0; si < selectedSeries.length; si++) {
        const s = selectedSeries[si];
        const b = s.buckets[bi];
        const val = b ? bucketTokens(b) : 0;
        if (val <= 0) continue;
        const h = Math.max(1.5, (val / yMax) * yH);
        curY -= h;
        segments.push({ slot: si, y: curY, h, key: s.seriesKey, val });
      }
      return { bi, cx, bx, barW, total: totals[bi], segments };
    });
    return { kind: "bars", xW, y0, yH, base, step, yTicks, bars, xLabels };
  }

  let maxY = metric === Metric.CACHE_HIT ? 100 : 10;
  if (metric !== Metric.CACHE_HIT) {
    let peak = 0;
    for (const s of selectedSeries) {
      const few = seriesIsFewSamples(s, metric);
      for (const b of s.buckets) {
        if (metric === Metric.OUTPUT_SPEED && bucketHasSpeed(b)) {
          peak = Math.max(peak, !few && b.speedP90 > 0 ? b.speedP90 : b.speedP50);
        } else if (metric === Metric.TTFT && bucketHasTtft(b)) {
          peak = Math.max(peak, !few && b.ttftP90Ms > 0 ? b.ttftP90Ms : b.ttftP50Ms);
        }
      }
    }
    maxY = Math.max(metric === Metric.TTFT ? 200 : 10, Math.ceil(peak * 1.15));
  }

  const yTicks: ChartTick[] =
    metric === Metric.CACHE_HIT
      ? [100, 50, 0].map((v, idx) => ({ y: idx === 0 ? y0 : idx === 1 ? y0 + yH / 2 : base, label: `${v}%` }))
      : [maxY, maxY / 2, 0].map((v, idx) => ({
          y: idx === 0 ? y0 : idx === 1 ? y0 + yH / 2 : base,
          label: v === 0 ? "0" : metric === Metric.TTFT ? formatTtftValue(v) : formatSpeedValue(v),
        }));

  const xAt = (bi: number) => (bucketCount <= 1 ? xW / 2 : x0 + (bi / (bucketCount - 1)) * xW);
  const yAt = (v: number) => base - Math.max(0, Math.min(1, v / maxY)) * yH;

  const lines: LineSeriesGeom[] = selectedSeries.map((s, slot) => {
    const few = seriesIsFewSamples(s, metric);
    const segments: LinePt[][] = [];
    let curSeg: LinePt[] = [];
    for (let bi = 0; bi < bucketCount; bi++) {
      const b = s.buckets[bi];
      let val: number | null = null;
      let low: number | null = null;
      let high: number | null = null;
      if (b) {
        if (metric === Metric.CACHE_HIT) {
          const r = bucketCacheHit(b);
          if (r !== null) val = r * 100;
        } else if (metric === Metric.OUTPUT_SPEED && bucketHasSpeed(b)) {
          val = b.speedP50;
          if (!few && b.speedP10 > 0 && b.speedP90 >= b.speedP10) {
            low = b.speedP10;
            high = b.speedP90;
          }
        } else if (metric === Metric.TTFT && bucketHasTtft(b)) {
          val = b.ttftP50Ms;
          if (!few && b.ttftP10Ms > 0 && b.ttftP90Ms >= b.ttftP10Ms) {
            low = b.ttftP10Ms;
            high = b.ttftP90Ms;
          }
        }
      }
      if (val === null) {
        if (curSeg.length > 0) {
          segments.push(curSeg);
          curSeg = [];
        }
      } else {
        curSeg.push({
          bi,
          x: xAt(bi),
          y: yAt(val),
          lowY: low !== null ? yAt(low) : undefined,
          highY: high !== null ? yAt(high) : undefined,
          val,
        });
      }
    }
    if (curSeg.length > 0) segments.push(curSeg);

    const linePaths = few
      ? []
      : segments
          .filter((seg) => seg.length >= 2)
          .map((seg) => seg.map((p, idx) => `${idx === 0 ? "M" : "L"}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(" "));

    const bandPolygons =
      few || slot >= 3 || metric === Metric.CACHE_HIT
        ? []
        : segments
            .filter((seg) => seg.length >= 2 && seg.every((p) => p.lowY !== undefined && p.highY !== undefined))
            .map((seg) => {
              const top = seg.map((p) => `${p.x.toFixed(1)},${p.highY!.toFixed(1)}`);
              const bot = [...seg].reverse().map((p) => `${p.x.toFixed(1)},${p.lowY!.toFixed(1)}`);
              return [...top, ...bot].join(" ");
            });

    return { series: s, slot, few, linePaths, bandPolygons, points: segments.flat() };
  });

  return { kind: "lines", xW, y0, yH, base, xAt, yTicks, lines, xLabels };
}
