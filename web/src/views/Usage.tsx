import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { AgentMark } from "../agent-mark";
import {
  clampUsageCustomRange,
  routeForUsage,
  useCoreHost,
  useUsageView,
  UsageMetricTab,
  UsageRangePreset,
  type Go,
} from "../core/host";
import { renderText } from "../core/text";
import { t, useLocale, type Locale } from "../i18n";
import { UsageMetricTab as Metric } from "../gen/remoteagent/core/v1/usage_view_pb";
import type { UsageSeriesView, UsageViewModel } from "../gen/remoteagent/core/v1/usage_view_pb";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { UsageSourceState } from "../gen/remoteagent/v1/local_pb";
import { ChevronRight } from "../icons";
import type { Remote } from "../remote";
import { Banner, NavBar, Segmented, useRemote } from "../ui";
import {
  bucketHasSpeed,
  bucketHasTtft,
  bucketCacheHit,
  bucketTokens,
  buildUsageChart,
  DEFAULT_OVERLAY_SERIES,
  formatDateLabel,
  MAX_OVERLAY_SERIES,
  seriesIsFewSamples,
  seriesTotalTokens,
} from "../usageChart";
import {
  formatCacheHit,
  formatCommaUint,
  formatSignedPercent,
  formatSignedPoints,
  formatSpeedValue,
  formatTokens,
  formatTtftDelta,
  formatTtftValue,
} from "../usageFormat";

const METRIC_OPTIONS: Array<{ value: string; tab: UsageMetricTab; label: (locale: Locale) => string }> = [
  { value: String(Metric.TOKENS), tab: Metric.TOKENS, label: (locale) => t("usage.tab_tokens", undefined, locale) },
  { value: String(Metric.CACHE_HIT), tab: Metric.CACHE_HIT, label: (locale) => t("usage.tab_cache_hit", undefined, locale) },
  { value: String(Metric.OUTPUT_SPEED), tab: Metric.OUTPUT_SPEED, label: (locale) => t("usage.tab_speed", undefined, locale) },
  { value: String(Metric.TTFT), tab: Metric.TTFT, label: (locale) => t("usage.tab_ttft", undefined, locale) },
];

const RANGE_OPTIONS: Array<{ value: string; preset: UsageRangePreset; label: (locale: Locale) => string }> = [
  { value: String(UsageRangePreset.TODAY), preset: UsageRangePreset.TODAY, label: (locale) => t("usage.range_today", undefined, locale) },
  { value: String(UsageRangePreset.SEVEN_DAYS), preset: UsageRangePreset.SEVEN_DAYS, label: (locale) => t("usage.range_7d", undefined, locale) },
  { value: String(UsageRangePreset.THIRTY_DAYS), preset: UsageRangePreset.THIRTY_DAYS, label: (locale) => t("usage.range_30d", undefined, locale) },
  { value: String(UsageRangePreset.CUSTOM), preset: UsageRangePreset.CUSTOM, label: (locale) => t("usage.range_custom", undefined, locale) },
];

function useLargeFontScale(): boolean {
  const [large, setLarge] = useState(() => {
    if (typeof window === "undefined" || typeof getComputedStyle !== "function") return false;
    return parseFloat(getComputedStyle(document.documentElement).fontSize) >= 24;
  });
  useEffect(() => {
    if (typeof window === "undefined") return;
    const check = () => setLarge(parseFloat(getComputedStyle(document.documentElement).fontSize) >= 24);
    check();
    window.addEventListener("resize", check);
    return () => window.removeEventListener("resize", check);
  }, []);
  return large;
}

function toLocalDateInputValue(ms: bigint | number): string {
  const d = new Date(Number(ms));
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

function fromLocalDateInputValue(s: string): bigint | undefined {
  const parts = s.split("-").map(Number);
  if (parts.length !== 3 || parts.some((p) => !Number.isFinite(p))) return undefined;
  return BigInt(new Date(parts[0], parts[1] - 1, parts[2]).getTime());
}

function formatFullDay(ms: bigint): string {
  const d = new Date(Number(ms));
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

function formatOfflineTime(ms: bigint): string {
  const d = new Date(Number(ms));
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

function formatTtftSummary(ms: number, locale: Locale): string {
  if (!Number.isFinite(ms) || ms <= 0) return "—";
  if (ms >= 60_000) {
    const totalSec = Math.round(ms / 1000);
    return t("usage.duration_min_sec", { min: Math.floor(totalSec / 60), sec: totalSec % 60 }, locale);
  }
  return formatTtftValue(ms);
}

function formatSeriesDeltaCell(s: UsageSeriesView, metric: UsageMetricTab, locale: Locale): string {
  if (s.previousBackfilling) return t("usage.backfilling_cell", undefined, locale);
  switch (metric) {
    case Metric.TOKENS:
      return s.hasTokensDelta ? formatSignedPercent(s.tokensDeltaPercent, locale) : "—";
    case Metric.CACHE_HIT:
      return s.hasCacheHitDelta ? formatSignedPoints(s.cacheHitDeltaPoints, locale) : "—";
    case Metric.OUTPUT_SPEED:
      return s.hasSpeedDelta ? formatSignedPercent(s.speedDeltaPercent, locale) : "—";
    default:
      return s.hasTtftDelta ? formatTtftDelta(s.current?.ttftP50Ms ?? 0, s.previous?.ttftP50Ms ?? 0, locale) : "—";
  }
}

function SwatchBar({ slot }: { slot: number }) {
  switch (slot) {
    case 0: {
      return <span className="u-bsw" style={{ background: "var(--ink)" }} aria-hidden="true" />;
    }
    case 1: {
      return <span className="u-bsw" style={{ background: "var(--bar-2)" }} aria-hidden="true" />;
    }
    case 2: {
      return (
        <svg className="u-bsw-svg" viewBox="0 0 12 12" aria-hidden="true">
          <defs>
            <pattern id="u-hatch-sw" width="4" height="4" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
              <rect width="4" height="4" fill="var(--gbg)" />
              <line x1="0" y1="0" x2="0" y2="4" stroke="var(--ink)" strokeWidth="1.4" />
            </pattern>
          </defs>
          <rect x=".5" y=".5" width="11" height="11" rx="2" fill="url(#u-hatch-sw)" stroke="var(--ink3)" strokeWidth=".45" />
        </svg>
      );
    }
    default: {
      return (
        <svg className="u-bsw-svg" viewBox="0 0 12 12" aria-hidden="true">
          <rect x="1" y="1" width="10" height="10" rx="2" className="u-bar4" />
        </svg>
      );
    }
  }
}

function SwatchLine({ slot }: { slot: number }) {
  return (
    <svg className="u-sw" viewBox="0 0 24 12" aria-hidden="true">
      <line x1="1" y1="6" x2="23" y2="6" className={`u-l${slot + 1}`} />
      <circle cx="12" cy="6" r="3" className={`u-m${slot + 1}`} />
    </svg>
  );
}

type ActiveSummary = {
  uncached: number;
  read: number;
  write: number;
  out: number;
  reas: number;
  total: number;
  usageReqs: number;
  speedReqs: number;
  hitRate: number;
  inDenom: number;
  tokDeltaPct: number | null;
  prevLabel: string;
  hasWeightedSpeed: boolean;
  hasOutlier: boolean;
  primary?: UsageSeriesView;
};

function UsageCardHeader({
  metric,
  pairHeadline,
  summary,
  anyFewSamples,
  locale,
}: {
  metric: UsageMetricTab;
  pairHeadline: string;
  summary: ActiveSummary;
  anyFewSamples: boolean;
  locale: Locale;
}) {
  const prim = summary.primary;
  if (metric === Metric.TOKENS) {
    const deltaStr = summary.tokDeltaPct === null ? "—" : formatSignedPercent(summary.tokDeltaPct, locale);
    const hasDenom = Boolean(summary.inDenom);
    const hitStr = hasDenom ? formatCacheHit(summary.hitRate) : "—";
    return (
      <div className="u-hd">
        <div>
          <div className="u-pair">{pairHeadline}</div>
          <div className="u-kpi">
            {formatTokens(summary.total, locale)}
            <small>{t("usage.kpi_unit_tokens", undefined, locale)}</small>
          </div>
        </div>
        <div className="u-sub">
          <div>{`${summary.prevLabel} ${deltaStr}`}</div>
          <div>{`${t("usage.tab_cache_hit", undefined, locale)} ${hitStr}`}</div>
        </div>
      </div>
    );
  }
  if (metric === Metric.CACHE_HIT) {
    const dStr = prim && prim.hasCacheHitDelta ? formatSignedPoints(prim.cacheHitDeltaPoints, locale) : "—";
    const mainVal = anyFewSamples ? "—" : summary.inDenom > 0 ? formatCacheHit(summary.hitRate) : "—";
    const subVal = anyFewSamples
      ? t("usage.few_samples_count", { count: summary.usageReqs }, locale)
      : `${summary.prevLabel} ${dStr}`;
    return (
      <div className="u-hd">
        <div>
          <div className="u-pair">{pairHeadline}</div>
          <div className="u-kpi">{mainVal}</div>
        </div>
        <div className="u-sub">
          <div>{t("usage.sub_weighted_cache", undefined, locale)}</div>
          <div>{subVal}</div>
        </div>
      </div>
    );
  }
  if (metric === Metric.OUTPUT_SPEED) {
    const c = prim?.current;
    const p10 = c?.hasSpeed ? formatSpeedValue(c.speedP10) : "—";
    const p90 = c?.hasSpeed ? formatSpeedValue(c.speedP90) : "—";
    const hasRecent = !!c && c.recent10SpeedCount > 0n && c.recent10SpeedP50 > 0;
    const recent10 = hasRecent ? formatSpeedValue(c!.recent10SpeedP50) : "";
    const sub1 = c?.hasSpeed ? t("usage.sub_median_p10_p90", { low: p10, high: p90 }, locale) : t("usage.sub_p10_p90", { low: p10, high: p90 }, locale);
    const sub2 = anyFewSamples
      ? t("usage.few_samples_count", { count: summary.speedReqs }, locale)
      : recent10
        ? t("usage.sub_recent10", { speed: recent10 }, locale)
        : prim
          ? formatSeriesDeltaCell(prim, Metric.OUTPUT_SPEED, locale)
          : "—";
    return (
      <div className="u-hd">
        <div>
          <div className="u-pair">{pairHeadline}</div>
          <div className="u-kpi">
            {c?.hasSpeed ? (
              <>
                {formatSpeedValue(c.speedP50)}
                <small>{t("usage.kpi_unit_toks", undefined, locale)}</small>
              </>
            ) : "—"}
          </div>
        </div>
        <div className="u-sub">
          <div>{sub1}</div>
          <div>{sub2}</div>
        </div>
      </div>
    );
  }
  const c = prim?.current;
  const p10 = c?.hasTtft ? formatTtftSummary(c.ttftP10Ms, locale) : "—";
  const p90 = c?.hasTtft ? formatTtftSummary(c.ttftP90Ms, locale) : "—";
  const sub2 = anyFewSamples
    ? t("usage.few_samples_count", { count: summary.speedReqs }, locale)
    : prim
      ? `${summary.prevLabel} ${formatSeriesDeltaCell(prim, Metric.TTFT, locale)}`
      : "—";
  return (
    <div className="u-hd">
      <div>
        <div className="u-pair">{pairHeadline}</div>
        <div className="u-kpi">{c?.hasTtft ? formatTtftSummary(c.ttftP50Ms, locale) : "—"}</div>
      </div>
      <div className="u-sub">
        <div>{t("usage.sub_p10_p90", { low: p10, high: p90 }, locale)}</div>
        <div>{sub2}</div>
      </div>
    </div>
  );
}

function UsageTableHead({ metric, wide, locale }: { metric: UsageMetricTab; wide: boolean; locale: Locale }) {
  switch (metric) {
    case Metric.TOKENS: {
      return (
        <tr>
          <th>{t("usage.col_pair", undefined, locale)}</th>
          <th>{t("usage.col_total", undefined, locale)}</th>
          {wide && (
            <>
              <th>{t("usage.col_uncached", undefined, locale)}</th>
              <th>{t("usage.col_cache_read", undefined, locale)}</th>
              <th>{t("usage.col_cache_write", undefined, locale)}</th>
              <th>{t("usage.col_output", undefined, locale)}</th>
            </>
          )}
          <th>{t("usage.col_reqs", undefined, locale)}</th>
          <th>{t("usage.col_vs_prev", undefined, locale)}</th>
        </tr>
      );
    }
    case Metric.CACHE_HIT: {
      return (
        <tr>
          <th>{t("usage.col_pair", undefined, locale)}</th>
          <th>{t("usage.col_hit_rate", undefined, locale)}</th>
          <th>{t("usage.col_reqs", undefined, locale)}</th>
          <th>{t("usage.col_vs_prev", undefined, locale)}</th>
        </tr>
      );
    }
    case Metric.OUTPUT_SPEED: {
      return (
        <tr>
          <th>{t("usage.col_pair", undefined, locale)}</th>
          <th>{t("usage.col_median", undefined, locale)}</th>
          <th>{t("usage.col_valid_reqs", undefined, locale)}</th>
          <th>{t("usage.col_vs_prev", undefined, locale)}</th>
        </tr>
      );
    }
    default: {
      return (
        <tr>
          <th>{t("usage.col_pair", undefined, locale)}</th>
          <th>{t("usage.col_ttft_median", undefined, locale)}</th>
          <th>{t("usage.col_valid_reqs", undefined, locale)}</th>
          <th>{t("usage.col_vs_prev", undefined, locale)}</th>
        </tr>
      );
    }
  }
}

function UsageTableMetricCells({
  s,
  metric,
  wide,
  writeText,
  locale,
}: {
  s: UsageSeriesView;
  metric: UsageMetricTab;
  wide: boolean;
  writeText: string;
  locale: Locale;
}) {
  const c = s.current;
  const hasReas = (c?.reasoningOutputTokens ?? 0n) > 0n;
  const unrepLabel = !s.cacheWriteSupported ? t("usage.unreported_aria", undefined, locale) : undefined;
  switch (metric) {
    case Metric.TOKENS: {
      return (
        <>
          <td>
            <b>{formatTokens(seriesTotalTokens(s), locale)}</b>
          </td>
          {wide && (
            <>
              <td>{formatTokens(c?.uncachedInputTokens ?? 0n, locale)}</td>
              <td>{formatTokens(c?.cacheReadTokens ?? 0n, locale)}</td>
              <td>
                <span aria-label={unrepLabel}>{writeText}</span>
              </td>
              <td>
                {formatTokens(c?.outputTokens ?? 0n, locale)}
                {hasReas && (
                  <span className="sub-td">
                    {t("usage.comp_reasoning", { value: formatTokens(c!.reasoningOutputTokens, locale) }, locale)}
                  </span>
                )}
              </td>
            </>
          )}
          <td>{formatCommaUint(c?.usageRequestCount ?? 0n)}</td>
          <td className="muted">{formatSeriesDeltaCell(s, metric, locale)}</td>
        </>
      );
    }
    case Metric.CACHE_HIT: {
      return (
        <>
          <td>
            <b>{c?.hasCacheHitRate ? formatCacheHit(c.cacheHitRate) : "—"}</b>
          </td>
          <td>{formatCommaUint(c?.usageRequestCount ?? 0n)}</td>
          <td className="muted">{formatSeriesDeltaCell(s, metric, locale)}</td>
        </>
      );
    }
    case Metric.OUTPUT_SPEED: {
      return (
        <>
          <td>
            <b>{c?.hasSpeed ? `${formatSpeedValue(c.speedP50)} ${t("usage.kpi_unit_toks", undefined, locale)}` : "—"}</b>
          </td>
          <td>{formatCommaUint(c?.speedRequestCount ?? 0n)}</td>
          <td className="muted">{formatSeriesDeltaCell(s, metric, locale)}</td>
        </>
      );
    }
    default: {
      return (
        <>
          <td>
            <b>{c?.hasTtft ? formatTtftSummary(c.ttftP50Ms, locale) : "—"}</b>
          </td>
          <td>{formatCommaUint(c?.speedRequestCount ?? 0n)}</td>
          <td className="muted">{formatSeriesDeltaCell(s, metric, locale)}</td>
        </>
      );
    }
  }
}

export function UsageScreenView({
  vm,
  wide = false,
  onBack,
  onSelectMetric,
  onSelectPreset,
  onSelectCustomRange,
  onRetry,
}: {
  vm: UsageViewModel;
  wide?: boolean;
  onBack: () => void;
  onSelectMetric: (metric: UsageMetricTab) => void;
  onSelectPreset: (preset: UsageRangePreset) => void;
  onSelectCustomRange: (startMs: bigint, endMs: bigint) => void;
  onRetry: () => void;
}) {
  const locale = useLocale();
  const largeFont = useLargeFontScale();
  const metric = vm.metric === Metric.UNSPECIFIED ? Metric.TOKENS : vm.metric;
  const preset = vm.preset === UsageRangePreset.UNSPECIFIED ? UsageRangePreset.SEVEN_DAYS : vm.preset;

  const [showSkeleton, setShowSkeleton] = useState(false);
  useEffect(() => {
    if (!vm.loading) {
      setShowSkeleton(false);
      return;
    }
    const timer = setTimeout(() => setShowSkeleton(true), 200);
    return () => clearTimeout(timer);
  }, [vm.loading]);

  const [customPickerOpen, setCustomPickerOpen] = useState(false);
  const clampedDefault = useMemo(() => clampUsageCustomRange(vm.startMs, vm.endMs > 0n ? vm.endMs - 86_400_000n : 0n), [vm.startMs, vm.endMs]);
  const [draftStart, setDraftStart] = useState(() => toLocalDateInputValue(clampedDefault.startMs));
  const [draftEnd, setDraftEnd] = useState(() => toLocalDateInputValue(clampedDefault.endMs));
  useEffect(() => {
    if (preset === UsageRangePreset.CUSTOM && vm.startMs > 0n && vm.endMs > 0n) {
      setDraftStart(toLocalDateInputValue(vm.startMs));
      setDraftEnd(toLocalDateInputValue(vm.endMs - 1n));
    }
  }, [preset, vm.startMs, vm.endMs]);

  const [selectedKeys, setSelectedKeys] = useState([] as string[]);
  const seriesKeysSig = vm.series.map((s) => `${s.seriesKey}:${s.focused ? 1 : 0}`).join("|");
  useEffect(() => {
    const initial = vm.series.slice(0, DEFAULT_OVERLAY_SERIES).map((s) => s.seriesKey);
    setSelectedKeys(initial);
  }, [seriesKeysSig]);

  const toggleSeriesOverlay = useCallback((key: string) => {
    setSelectedKeys((prev) => {
      const idx = prev.indexOf(key);
      if (idx >= 0) {
        if (prev.length <= 1) return prev;
        return prev.filter((k) => k !== key);
      }
      if (prev.length < MAX_OVERLAY_SERIES) return [...prev, key];
      return [...prev.slice(0, MAX_OVERLAY_SERIES - 1), key];
    });
  }, []);

  const selectedSeries = useMemo(() => {
    const byKey = new Map(vm.series.map((s) => [s.seriesKey, s]));
    const picked = selectedKeys.map((k) => byKey.get(k)).filter((s): s is UsageSeriesView => !!s);
    return picked.length > 0 ? picked : vm.series.slice(0, 1);
  }, [vm.series, selectedKeys]);

  const [scrubIdx, setScrubIdx] = useState(null as number | null);
  useEffect(() => {
    setScrubIdx(null);
  }, [preset, metric, vm.startMs, vm.endMs]);

  const bucketCount = useMemo(() => {
    let maxLen = 0;
    for (const s of selectedSeries) {
      if (s.buckets.length > maxLen) maxLen = s.buckets.length;
    }
    return maxLen;
  }, [selectedSeries]);

  const svgRef = useRef(null as SVGSVGElement | null);
  const hatchId = "u-hatch";

  const updateScrubFromClientX = useCallback(
    (clientX: number) => {
      if (!svgRef.current || bucketCount <= 0) return;
      const rect = svgRef.current.getBoundingClientRect();
      if (rect.width <= 0) return;
      const plotWidth = rect.width * (294 / 332);
      const rel = Math.max(0, Math.min(1, (clientX - rect.left) / Math.max(1, plotWidth)));
      const idx = Math.min(bucketCount - 1, Math.max(0, Math.floor(rel * bucketCount)));
      setScrubIdx(idx);
    },
    [bucketCount],
  );

  const onChartPointerDown = (e: PointerEvent<SVGSVGElement>) => {
    if (bucketCount <= 0) return;
    e.currentTarget.setPointerCapture?.(e.pointerId);
    updateScrubFromClientX(e.clientX);
  };

  const onChartPointerMove = (e: PointerEvent<SVGSVGElement>) => {
    if (scrubIdx === null && e.buttons === 0 && e.pointerType !== "mouse") return;
    if (e.buttons > 0 || scrubIdx !== null) updateScrubFromClientX(e.clientX);
  };

  const onChartKeyDown = (e: KeyboardEvent<SVGSVGElement>) => {
    if (bucketCount <= 0) return;
    if (e.key === "ArrowLeft") {
      e.preventDefault();
      setScrubIdx((cur) => (cur === null ? bucketCount - 1 : Math.max(0, cur - 1)));
    } else if (e.key === "ArrowRight") {
      e.preventDefault();
      setScrubIdx((cur) => (cur === null ? 0 : Math.min(bucketCount - 1, cur + 1)));
    } else if (e.key === "Escape") {
      setScrubIdx(null);
    }
  };

  const name = vm.machineName || vm.machineId;
  const subtitle = vm.online ? t("usage.subtitle_live", { name }, locale) : t("usage.subtitle_offline", { name }, locale);

  const rangeMinStr = useMemo(() => {
    const today = new Date();
    const minDate = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 89);
    return toLocalDateInputValue(minDate.getTime());
  }, []);
  const rangeMaxStr = useMemo(() => toLocalDateInputValue(Date.now()), []);

  const customDays = useMemo(() => {
    if (vm.startMs <= 0n || vm.endMs <= vm.startMs) return 1;
    return Math.max(1, Math.round(Number(vm.endMs - vm.startMs) / 86_400_000));
  }, [vm.startMs, vm.endMs]);

  const applyCustomDates = (nextStart: string, nextEnd: string) => {
    setDraftStart(nextStart);
    setDraftEnd(nextEnd);
    const s = fromLocalDateInputValue(nextStart);
    const e = fromLocalDateInputValue(nextEnd);
    if (s !== undefined && e !== undefined) {
      const clamped = clampUsageCustomRange(s, e);
      onSelectCustomRange(clamped.startMs, clamped.endMs);
    }
  };

  const allSourcesUnavailable =
    vm.found &&
    vm.supported &&
    ((vm.sources.length > 0 && vm.sources.every((s) => s.state === UsageSourceState.UNAVAILABLE)) ||
      (vm.series.length === 0 && vm.unsupportedFootnote?.key === "usage.only_unavailable_title"));
  const unavailAgentNames =
    vm.sources
      .filter((s) => s.state === UsageSourceState.UNAVAILABLE)
      .map((s) => (s.agent === Agent.ANTIGRAVITY ? "Antigravity" : s.agent === Agent.CLAUDE ? "Claude Code" : "Codex"))
      .join(", ") || "Antigravity";

  const activeSummary: ActiveSummary = useMemo(() => {
    let uncached = 0;
    let read = 0;
    let write = 0;
    let out = 0;
    let reas = 0;
    let prevTok = 0;
    let usageReqs = 0;
    let speedReqs = 0;
    let hasWeightedSpeed = false;
    let hasOutlier = false;

    for (const s of selectedSeries) {
      const c = s.current;
      if (!c) continue;
      uncached += Number(c.uncachedInputTokens);
      read += Number(c.cacheReadTokens);
      write += Number(c.cacheWriteTokens);
      out += Number(c.outputTokens);
      reas += Number(c.reasoningOutputTokens);
      usageReqs += Number(c.usageRequestCount);
      speedReqs += Number(c.speedRequestCount);
      if (s.previous) {
        prevTok += Number(s.previous.uncachedInputTokens + s.previous.cacheReadTokens + s.previous.cacheWriteTokens + s.previous.outputTokens);
      }
      if (c.hasSpeed) hasWeightedSpeed = true;
      if (c.speedP50 > 0 && c.speedP90 > c.speedP50 * 4) hasOutlier = true;
      if (c.ttftP50Ms > 0 && c.ttftP90Ms > c.ttftP50Ms * 4) hasOutlier = true;
    }
    const total = uncached + read + write + out;
    const inDenom = uncached + read + write;
    const hitRate = inDenom > 0 ? read / inDenom : 0;
    const prevLabel =
      preset === UsageRangePreset.SEVEN_DAYS
        ? t("usage.sub_vs_prev_7d", undefined, locale)
        : preset === UsageRangePreset.THIRTY_DAYS
          ? t("usage.sub_vs_prev_30d", undefined, locale)
          : t("usage.sub_vs_prev_generic", undefined, locale);
    const anyPrevBf = selectedSeries.some((s) => s.previousBackfilling);
    const tokDeltaPct = !anyPrevBf && usageReqs >= 3 && prevTok > 0 ? ((total - prevTok) / prevTok) * 100 : null;
    return {
      uncached,
      read,
      write,
      out,
      reas,
      total,
      usageReqs,
      speedReqs,
      hitRate,
      inDenom,
      tokDeltaPct,
      prevLabel,
      hasWeightedSpeed,
      hasOutlier,
      primary: selectedSeries[0],
    };
  }, [selectedSeries, preset, locale]);

  const anySelectedFewSamples =
    metric !== Metric.TOKENS && selectedSeries.length > 0 && selectedSeries.every((s) => seriesIsFewSamples(s, metric));

  const pairHeadline = useMemo(() => {
    if (selectedSeries.length === 1) {
      const s = selectedSeries[0];
      return `${s.agentName} · ${s.model}`;
    }
    if (selectedKeys.length >= MAX_OVERLAY_SERIES && vm.series.length > MAX_OVERLAY_SERIES) {
      return t("usage.pairs_capped_summary", { count: vm.series.length }, locale);
    }
    if (selectedSeries.length < vm.series.length) {
      return t("usage.pairs_summary", { name, count: vm.series.length }, locale);
    }
    return `${name} · ${t("usage.filter_all", undefined, locale)}`;
  }, [selectedSeries, selectedKeys.length, vm.series.length, name, locale]);

  const chartData = useMemo(
    () => buildUsageChart(selectedSeries, bucketCount, metric, vm.granularity, locale),
    [selectedSeries, bucketCount, metric, vm.granularity, locale],
  );

  const compTotal = Math.max(1, activeSummary.total);
  const compItems = [
    { key: "cr", cls: "cr", color: "var(--comp-r)", label: t("usage.comp_cache_read", undefined, locale), val: activeSummary.read, unreported: false },
    { key: "ci", cls: "ci", color: "var(--comp-i)", label: t("usage.comp_uncached", undefined, locale), val: activeSummary.uncached, unreported: false },
    { key: "cw", cls: "cw", color: "var(--faint)", label: t("usage.comp_cache_write", undefined, locale), val: activeSummary.write, unreported: !selectedSeries.some((s) => s.cacheWriteSupported) && activeSummary.write === 0 },
    { key: "co", cls: "co", color: "var(--ink)", label: t("usage.comp_output", undefined, locale), val: activeSummary.out, unreported: false },
  ];

  const scrubReadout = useMemo(() => {
    if (scrubIdx === null || !selectedSeries.length) return null;
    const bRef = selectedSeries[0].buckets[scrubIdx];
    const timeLabel = bRef ? formatDateLabel(bRef.startMs, vm.granularity) : "";
    const parts = selectedSeries.map((s) => {
      const b = s.buckets[scrubIdx];
      let valStr = "—";
      if (b) {
        if (metric === Metric.TOKENS) {
          const tok = bucketTokens(b);
          valStr = tok > 0 ? formatTokens(tok, locale) : "0";
        } else if (metric === Metric.CACHE_HIT) {
          const r = bucketCacheHit(b);
          valStr = r !== null ? formatCacheHit(r) : "—";
        } else if (metric === Metric.OUTPUT_SPEED) {
          valStr = bucketHasSpeed(b) ? `${formatSpeedValue(b.speedP50)} ${t("usage.kpi_unit_toks", undefined, locale)}` : "—";
        } else if (bucketHasTtft(b)) {
          valStr = formatTtftSummary(b.ttftP50Ms, locale);
        }
      }
      return { name: s.model || s.agentName, valStr, key: s.seriesKey };
    });
    const totalTok = metric === Metric.TOKENS ? selectedSeries.reduce((acc, s) => acc + (s.buckets[scrubIdx] ? bucketTokens(s.buckets[scrubIdx]) : 0), 0) : 0;
    return { timeLabel, parts, totalTok };
  }, [scrubIdx, selectedSeries, metric, vm.granularity, locale]);

  const metricLabel = (METRIC_OPTIONS.find((m) => m.tab === metric) ?? METRIC_OPTIONS[0]).label(locale);
  const chartA11yLabel =
    metric === Metric.TOKENS
      ? t("usage.chart_a11y_tokens", { name, count: selectedSeries.length, total: formatTokens(activeSummary.total, locale) }, locale)
      : t("usage.chart_a11y_line", { name, metric: metricLabel, count: selectedSeries.length }, locale);

  const errStr = renderText(vm.errorText);
  const showOfflineEmpty = vm.found && vm.supported && !vm.online && !vm.cachedOffline && vm.series.length === 0 && !vm.loading;
  const showGenericEmpty = vm.found && vm.supported && vm.online && vm.series.length === 0 && !vm.loading && !errStr && !allSourcesUnavailable;
  const customSummary =
    vm.startMs > 0n && vm.endMs > 0n
      ? t("usage.custom_range_summary", { start: formatFullDay(vm.startMs), end: formatFullDay(vm.endMs - 1n), days: customDays }, locale)
      : t(
          "usage.custom_range_summary",
          {
            start: formatFullDay(clampedDefault.startMs),
            end: formatFullDay(clampedDefault.endMs),
            days: Math.max(1, Math.round(Number(clampedDefault.endMs - clampedDefault.startMs) / 86_400_000) + 1),
          },
          locale,
        );
  const hasSeries = Boolean(vm.series.length);
  const hasBanners = vm.cachedOffline || vm.backfilling || (Boolean(errStr) && hasSeries);
  const showCard = vm.found && vm.supported && hasSeries;
  const tableCls = metric === Metric.TOKENS ? "u-tbl" : "u-tbl mid";
  const pageZoom = largeFont ? "200" : undefined;

  return (
    <div className="page set usage-page" data-u-zoom={pageZoom}>
      <NavBar onBack={onBack} title={t("usage.title", undefined, locale)} sub={subtitle} />
      <div className="body u-body">
        <div className="u-in">
          <div className="u-ctrl">
            <Segmented
              className="u-tabs"
              label={t("usage.title", undefined, locale)}
              value={String(metric)}
              onChange={(next) => onSelectMetric(Number(next) as UsageMetricTab)}
              options={METRIC_OPTIONS.map((o) => ({ value: o.value, label: o.label(locale) }))}
            />
            <Segmented
              className="u-ranges"
              label={t("usage.range_7d", undefined, locale)}
              value={String(preset)}
              onChange={(next) => {
                const p = Number(next) as UsageRangePreset;
                onSelectPreset(p);
                setCustomPickerOpen(p === UsageRangePreset.CUSTOM);
              }}
              options={RANGE_OPTIONS.map((o) => ({ value: o.value, label: o.label(locale) }))}
            />
          </div>

          {preset === UsageRangePreset.CUSTOM && (
            <>
              <div className="u-range-row">
                <button
                  type="button"
                  className="u-range"
                  aria-expanded={customPickerOpen}
                  onClick={() => setCustomPickerOpen((o) => !o)}
                >
                  <span>{customSummary}</span>
                  <span className="u-range-act">
                    {t("usage.custom_range_change", undefined, locale)}
                    <ChevronRight />
                  </span>
                </button>
              </div>
              {customPickerOpen && (
                <div className="u-range-pick">
                  <div className="u-range-fields">
                    <label>
                      {t("usage.custom_start_label", undefined, locale)}
                      <input
                        type="date"
                        min={rangeMinStr}
                        max={rangeMaxStr}
                        value={draftStart}
                        onChange={(e) => applyCustomDates(e.target.value, draftEnd)}
                      />
                    </label>
                    <label>
                      {t("usage.custom_end_label", undefined, locale)}
                      <input
                        type="date"
                        min={rangeMinStr}
                        max={rangeMaxStr}
                        value={draftEnd}
                        onChange={(e) => applyCustomDates(draftStart, e.target.value)}
                      />
                    </label>
                  </div>
                  <div className="u-range-hint">{t("usage.custom_range_limit_hint", undefined, locale)}</div>
                </div>
              )}
            </>
          )}

          {hasBanners && (
            <div className="u-banners">
              {vm.cachedOffline && (
                <Banner kind="warn">
                  {t("usage.offline_banner", { name, time: formatOfflineTime(vm.cachedAtMs) }, locale)}
                </Banner>
              )}
              {vm.backfilling && (
                <Banner>
                  {t("usage.backfilling_banner", { scanned: vm.backfillScannedDays, total: vm.backfillTotalDays || 90 }, locale)}
                </Banner>
              )}
              {errStr && vm.series.length > 0 && (
                <Banner kind="err" action={t("common.retry", undefined, locale)} onAction={onRetry}>
                  {`${t("usage.load_error_title", undefined, locale)}：${errStr}`}
                </Banner>
              )}
            </div>
          )}

          {!vm.found && vm.loading && showSkeleton && <div className="u-skel" aria-busy="true" />}

          {vm.found && !vm.supported && (
            <div className="u-empty">
              <b>{t("usage.unsupported_title", undefined, locale)}</b>
              <p>{t("usage.unsupported_body", { name }, locale)}</p>
            </div>
          )}

          {showOfflineEmpty && (
            <div className="u-empty">
              <b>{t("usage.offline_empty_title", { name }, locale)}</b>
              <p>{t("usage.offline_empty_body", { name }, locale)}</p>
            </div>
          )}

          {vm.found && vm.supported && errStr && vm.series.length === 0 && (
            <div className="u-empty">
              <b>{t("usage.load_error_title", undefined, locale)}</b>
              <p>{errStr}</p>
              <button type="button" className="btn pri" onClick={onRetry}>{t("common.retry", undefined, locale)}</button>
            </div>
          )}

          {vm.found && vm.supported && vm.loading && showSkeleton && vm.series.length === 0 && (
            <div className="u-skel" aria-busy="true" />
          )}

          {allSourcesUnavailable && (
            <div className="u-empty">
              <b>{t("usage.only_unavailable_title", { agents: unavailAgentNames }, locale)}</b>
              <p>{t("usage.only_unavailable_body", { agents: unavailAgentNames }, locale)}</p>
            </div>
          )}

          {showGenericEmpty && (
            <div className="u-empty">
              <b>{t("usage.empty_title", undefined, locale)}</b>
              <p>{t("usage.empty_body", undefined, locale)}</p>
            </div>
          )}

          {showCard && (
            <>
              <section className="u-card">
                <UsageCardHeader
                  metric={metric}
                  pairHeadline={pairHeadline}
                  summary={activeSummary}
                  anyFewSamples={anySelectedFewSamples}
                  locale={locale}
                />
                {vm.backfilling && vm.previousPeriodBackfilling && (
                  <div className="u-bf-zone">
                    {t("usage.backfilled_summary", { name, scanned: vm.backfillScannedDays }, locale)} · {t("usage.backfilling_zone", undefined, locale)}
                  </div>
                )}

                <div className="u-plot">
                  <svg
                    ref={svgRef}
                    className="u-svg"
                    viewBox="0 0 332 146"
                    role="img"
                    tabIndex={0}
                    aria-label={chartA11yLabel}
                    onPointerDown={onChartPointerDown}
                    onPointerMove={onChartPointerMove}
                    onKeyDown={onChartKeyDown}
                  >
                    <defs>
                      <pattern id={hatchId} width="5" height="5" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
                        <rect width="5" height="5" fill="var(--gbg)" />
                        <line x1="0" y1="0" x2="0" y2="5" stroke="var(--ink)" strokeWidth="1.6" />
                      </pattern>
                    </defs>
                    {chartData && (
                      <>
                        {chartData.yTicks.map((tick) => (
                          <g key={tick.y}>
                            <line x1="0" y1={tick.y} x2={chartData.xW} y2={tick.y} className="u-grid" />
                            <text x={chartData.xW + 6} y={tick.y + 3.5} className="u-ytxt">{tick.label}</text>
                          </g>
                        ))}
                        {chartData.kind === "bars" &&
                          chartData.bars.map((bar) => (
                            <g key={bar.bi} className={scrubIdx !== null && scrubIdx !== bar.bi ? "u-dim" : undefined}>
                              {bar.segments.map((seg) => (
                                <rect
                                  key={seg.key}
                                  x={bar.bx}
                                  y={seg.y}
                                  width={bar.barW}
                                  height={seg.h}
                                  rx="1.5"
                                  className={`u-bar${seg.slot + 1}`}
                                />
                              ))}
                            </g>
                          ))}
                        {chartData.kind === "lines" &&
                          chartData.lines.map((line) => (
                            <g key={line.series.seriesKey}>
                              {line.bandPolygons.map((pts, idx) => (
                                <polygon key={idx} points={pts} className={`u-b${Math.min(3, line.slot + 1)}`} />
                              ))}
                              {line.linePaths.map((d, idx) => (
                                <path key={idx} d={d} className={`u-l${line.slot + 1}`} />
                              ))}
                              {line.points.map((pt) => (
                                <circle
                                  key={pt.bi}
                                  cx={pt.x}
                                  cy={pt.y}
                                  r={line.few || scrubIdx === pt.bi ? 3.5 : 2.3}
                                  className={`u-m${line.slot + 1}`}
                                />
                              ))}
                            </g>
                          ))}
                        {scrubIdx !== null && (
                          <line
                            x1={chartData.kind === "bars" ? chartData.bars[scrubIdx]?.cx ?? 0 : chartData.xAt(scrubIdx)}
                            y1={chartData.y0}
                            x2={chartData.kind === "bars" ? chartData.bars[scrubIdx]?.cx ?? 0 : chartData.xAt(scrubIdx)}
                            y2={chartData.base}
                            className="u-scrub"
                          />
                        )}
                      </>
                    )}
                  </svg>
                  {chartData && chartData.xLabels.length > 0 && (
                    <div className="u-xaxis">
                      {chartData.xLabels.map((lbl, idx) => (
                        <span key={idx}>{lbl}</span>
                      ))}
                    </div>
                  )}
                  {scrubReadout && (
                    <div className="u-readout" aria-live="polite">
                      <span>{scrubReadout.timeLabel}</span>
                      {metric === Metric.TOKENS && <b>{t("usage.scrub_total", { total: formatTokens(scrubReadout.totalTok, locale) }, locale)}</b>}
                      {scrubReadout.parts.map((pt) => (
                        <span key={pt.key}>{`${pt.name}: ${pt.valStr}`}</span>
                      ))}
                    </div>
                  )}
                  {anySelectedFewSamples && (
                    <div className="u-few-note">{t("usage.few_samples_hint", undefined, locale)}</div>
                  )}
                </div>

                {metric === Metric.TOKENS && (
                  <div className="u-comp">
                    <div className="u-cbar" aria-hidden="true">
                      {compItems.map((it) =>
                        it.val > 0 ? <i key={it.key} className={it.cls} style={{ width: `${(it.val / compTotal) * 100}%` }} /> : null,
                      )}
                    </div>
                    <div className="u-cleg">
                      {compItems.map((it) => (
                        <span key={it.key}>
                          <i className="u-sq" style={{ background: it.color }} />
                          {it.label}{" "}
                          <b aria-label={it.unreported ? t("usage.unreported_aria", undefined, locale) : undefined}>
                            {it.unreported ? "—" : formatTokens(it.val, locale)}
                          </b>
                        </span>
                      ))}
                      {activeSummary.reas > 0 && (
                        <span>{t("usage.comp_reasoning", { value: formatTokens(activeSummary.reas, locale) }, locale)}</span>
                      )}
                    </div>
                  </div>
                )}
              </section>

              {largeFont ? (
                <div className="u-rows2x">
                  {vm.series.map((s) => {
                    const slot = selectedSeries.findIndex((p) => p.seriesKey === s.seriesKey);
                    const c = s.current;
                    const tot = seriesTotalTokens(s);
                    const sub = c
                      ? t(
                          "usage.row_sub_tokens",
                          {
                            inp: formatTokens(c.uncachedInputTokens, locale),
                            read: formatTokens(c.cacheReadTokens, locale),
                            write: s.cacheWriteSupported ? formatTokens(c.cacheWriteTokens, locale) : "—",
                            out: formatTokens(c.outputTokens, locale),
                          },
                          locale,
                        )
                      : "";
                    const valText =
                      metric === Metric.TOKENS
                        ? formatTokens(tot, locale)
                        : metric === Metric.CACHE_HIT
                          ? c?.hasCacheHitRate ? formatCacheHit(c.cacheHitRate) : "—"
                          : metric === Metric.OUTPUT_SPEED
                            ? c?.hasSpeed ? `${formatSpeedValue(c.speedP50)} ${t("usage.kpi_unit_toks", undefined, locale)}` : "—"
                            : c?.hasTtft ? formatTtftSummary(c.ttftP50Ms, locale) : "—";
                    return (
                      <button
                        key={s.seriesKey}
                        type="button"
                        className="u-row2"
                        aria-pressed={slot >= 0}
                        onClick={() => toggleSeriesOverlay(s.seriesKey)}
                      >
                        <div className="t1">
                          <span>{`${s.agentName} · ${s.model}`}</span>
                          <span>{valText}</span>
                        </div>
                        {sub && <div className="t2">{sub}</div>}
                        <div className="t3">
                          <span>{t("usage.entry_reqs", { count: Number(c?.usageRequestCount ?? 0n) }, locale)}</span>
                          <span>{formatSeriesDeltaCell(s, metric, locale)}</span>
                        </div>
                      </button>
                    );
                  })}
                </div>
              ) : (
                <table className={tableCls}>
                  <thead>
                    <UsageTableHead metric={metric} wide={wide} locale={locale} />
                  </thead>
                  <tbody>
                    {vm.series.map((s) => {
                      const slot = selectedSeries.findIndex((p) => p.seriesKey === s.seriesKey);
                      const isSel = slot >= 0;
                      const c = s.current;
                      const writeText = s.cacheWriteSupported ? formatTokens(c?.cacheWriteTokens ?? 0n, locale) : "—";
                      let rowSub = "";
                      if (metric === Metric.TOKENS) {
                        if (!isSel) rowSub = t("usage.row_sub_tap_overlay", undefined, locale);
                        else if (!wide && c) {
                          const args = {
                            inp: formatTokens(c.uncachedInputTokens, locale),
                            read: formatTokens(c.cacheReadTokens, locale),
                            write: writeText,
                            out: formatTokens(c.outputTokens, locale),
                          };
                          rowSub =
                            c.reasoningOutputTokens > 0n
                              ? t("usage.row_sub_tokens_reasoning", { ...args, reas: formatTokens(c.reasoningOutputTokens, locale) }, locale)
                              : t("usage.row_sub_tokens", args, locale);
                        }
                      } else if (metric === Metric.CACHE_HIT && c) {
                        rowSub = isSel
                          ? t("usage.row_sub_cache", {
                              read: formatTokens(c.cacheReadTokens, locale),
                              inp: formatTokens(c.uncachedInputTokens, locale),
                              write: writeText,
                            }, locale)
                          : t("usage.row_sub_tap_overlay", undefined, locale);
                      } else if (c) {
                        rowSub = isSel
                          ? t("usage.row_sub_speed", {
                              low: c.hasSpeed ? formatSpeedValue(c.speedP10) : "—",
                              high: c.hasSpeed ? formatSpeedValue(c.speedP90) : "—",
                              ttft: c.hasTtft ? formatTtftSummary(c.ttftP50Ms, locale) : "—",
                            }, locale)
                          : t("usage.row_sub_tap_overlay", undefined, locale);
                      }

                      return (
                        <tr
                          key={s.seriesKey}
                          className={isSel ? undefined : "off-s"}
                          aria-selected={isSel}
                          onClick={() => toggleSeriesOverlay(s.seriesKey)}
                        >
                          <td>
                            <button
                              type="button"
                              className="u-row-btn"
                              aria-pressed={isSel}
                              onClick={(e) => {
                                e.stopPropagation();
                                toggleSeriesOverlay(s.seriesKey);
                              }}
                            >
                              <span className="u-who">
                                {isSel ? (
                                  metric === Metric.TOKENS ? <SwatchBar slot={slot} /> : <SwatchLine slot={slot} />
                                ) : null}
                                <AgentMark agent={s.agent} named />
                                <span className="nm">
                                  <b>{`${s.agentName} · ${s.model}`}</b>
                                  {rowSub && <small>{rowSub}</small>}
                                </span>
                              </span>
                            </button>
                          </td>
                          <UsageTableMetricCells s={s} metric={metric} wide={wide} writeText={writeText} locale={locale} />
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              )}

              <div className="u-foots">
                {vm.showCacheWriteFootnote && <p>{t("usage.cache_write_footnote", undefined, locale)}</p>}
                <p>{t("usage.cache_formula_footnote", undefined, locale)}</p>
                <p>{t("usage.speed_threshold_footnote", undefined, locale)}</p>
                {activeSummary.hasOutlier && <p>{t("usage.outlier_note", undefined, locale)}</p>}
                {renderText(vm.unsupportedFootnote) && <p>{renderText(vm.unsupportedFootnote)}</p>}
                <p>{t("usage.passive_footnote", undefined, locale)}</p>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

export function UsagePage({
  remote,
  machineId,
  focusAgent,
  focusModel,
  wide = false,
  go,
  onBack,
}: {
  remote: Remote;
  machineId: string;
  focusAgent?: Agent;
  focusModel?: string;
  wide?: boolean;
  go?: Go;
  onBack: () => void;
}) {
  useRemote(remote);
  const host = useCoreHost(remote, go);
  const [metric, setMetric] = useState(Metric.TOKENS as UsageMetricTab);
  const [preset, setPreset] = useState(UsageRangePreset.SEVEN_DAYS as UsageRangePreset);
  const [customRange, setCustomRange] = useState(() => clampUsageCustomRange(0n, 0n));
  const vm = useUsageView(host, {
    machineId,
    focusAgent,
    focusModel,
    preset,
    customStartMs: preset === UsageRangePreset.CUSTOM ? customRange.startMs : 0n,
    customEndMs: preset === UsageRangePreset.CUSTOM ? customRange.endMs : 0n,
    metric,
  });

  return (
    <UsageScreenView
      vm={vm}
      wide={wide}
      onBack={onBack}
      onSelectMetric={setMetric}
      onSelectPreset={setPreset}
      onSelectCustomRange={(startMs, endMs) => {
        setCustomRange({ startMs, endMs });
        setPreset(UsageRangePreset.CUSTOM);
      }}
      onRetry={() => host.retryUsageScreen(machineId)}
    />
  );
}

export { routeForUsage };
