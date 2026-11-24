import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { AgentMark } from "../agent-mark";
import {
  clampUsageCustomRange,
  routeForUsage,
  UsageSortColumn,
  useCoreHost,
  useUsageView,
  UsageRangePreset,
  type Go,
} from "../core/host";
import { renderText } from "../core/text";
import { t, useLocale, type Locale } from "../i18n";
import {
  UsageEmptyKind,
  type UsageBarBucketView,
  type UsageMetricStripView,
  type UsageModelRowView,
  type UsageViewModel,
} from "../gen/remoteagent/core/v1/usage_view_pb";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import type { Remote } from "../remote";
import { Banner, NavBar, Segmented } from "../ui";
import {
  buildStripPaths,
  buildUsageAxisTicks,
  formatUsageBucketLabel,
  formatUsageCustomRange,
} from "../usageChart";

function middleEllipsis(name: string, max = 38): string {
  if (name.length <= max || max < 8) return name;
  const tail = Math.min(12, Math.max(4, Math.floor((max - 3) / 3)));
  const head = max - tail - 1;
  return `${name.slice(0, head)}…${name.slice(-tail)}`;
}

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

function ChevronDownSmall() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true">
      <path d="m6 9 6 6 6-6" />
    </svg>
  );
}

function ClearXIcon() {
  return (
    <svg viewBox="0 0 12 12" aria-hidden="true">
      <path d="M2.5 2.5l7 7M9.5 2.5l-7 7" />
    </svg>
  );
}

function HeroSection({
  vm,
  twoCol,
  locale,
  onClearSeries,
}: {
  vm: UsageViewModel;
  twoCol: boolean;
  locale: Locale;
  onClearSeries: () => void;
}) {
  const hero = vm.hero;
  const comp = hero?.composition;
  const compRowHeight = twoCol ? 18 : 17;
  const minCompHeight = Math.max(0, hero?.periodCompositionRows ?? 0) * compRowHeight + 5;

  const compItems = comp
    ? [
        { key: "uncached", label: t("usage.comp_uncached", undefined, locale), text: comp.uncachedInputText },
        { key: "read", label: t("usage.comp_cache_read", undefined, locale), text: comp.cacheReadText },
        { key: "write", label: t("usage.comp_cache_write", undefined, locale), text: comp.cacheWriteText },
        { key: "out", label: t("usage.comp_output", undefined, locale), text: comp.outputText },
        { key: "reas", label: t("usage.comp_reasoning_label", undefined, locale), text: comp.reasoningOutputText, indent: true },
      ].filter((it) => !!it.text)
    : [];

  let scopeNode: React.ReactNode;
  if (!vm.hasAnyUsage || !hero) {
    scopeNode = renderText(hero?.scopeText, locale) || t("usage.scope_all_empty", undefined, locale);
  } else if (hero.scrubbing) {
    const label = formatUsageBucketLabel(hero.scrubStartMs, hero.scrubEndMs, vm.granularity, locale);
    scopeNode = (
      <>
        <span className="nm">{label}</span>
        {hero.hasSelectedModel && <span>{` · ${middleEllipsis(hero.selectedModel, 22)}`}</span>}
      </>
    );
  } else if (hero.hasSelectedModel) {
    const selectedLabel = renderText(hero.scopeText, locale) || hero.selectedModel;
    scopeNode = (
      <>
        <AgentMark agent={hero.selectedAgent} />
        <span className="nm" title={selectedLabel}>
          {middleEllipsis(selectedLabel, 46)}
        </span>
        <button
          type="button"
          className="u-x"
          aria-label={t("usage.scope_clear_a11y", undefined, locale)}
          onClick={onClearSeries}
        >
          <ClearXIcon />
        </button>
      </>
    );
  } else {
    scopeNode = renderText(hero.scopeText, locale);
  }

  const subline = vm.hasAnyUsage && hero ? renderText(hero.sublineText, locale) : "";
  const bigStr = renderText(hero?.totalTokensText, locale) || "0";

  return (
    <>
      <div className="u-scope">{scopeNode}</div>
      <div className="u-hero">
        <div style={{ minWidth: 0 }}>
          <div className={vm.hasAnyUsage ? "u-big" : "u-big empty"}>
            {bigStr}
            <small>{t("usage.kpi_unit_tokens", undefined, locale)}</small>
          </div>
          <div className="u-sub">{subline}</div>
        </div>
        {vm.hasAnyUsage && (
          <div className="u-comp" style={{ minHeight: `${minCompHeight}px` }}>
            {compItems.map((it) => (
              <div key={it.key} className={it.indent ? "u-comp-row in2" : "u-comp-row"}>
                <span>{it.label}</span>
                <b>{renderText(it.text, locale)}</b>
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}

function MetricStrip({
  label,
  unit,
  strip,
  locale,
  scrubbing,
  scrubIdx,
  plotW,
  height,
  noSampleA11y,
}: {
  label: string;
  unit?: string;
  strip: UsageMetricStripView | undefined;
  locale: Locale;
  scrubbing: boolean;
  scrubIdx: number;
  plotW: number;
  height: number;
  noSampleA11y: string;
}) {
  if (!strip || !strip.hasRange) {
    return (
      <div className="u-strip">
        <div className="u-strip-h">
          <span>{label}</span>
          <b className="na" aria-label={noSampleA11y}>
            –
          </b>
        </div>
        <svg viewBox={`0 0 ${plotW} ${height}`} height={height} aria-hidden="true" />
      </div>
    );
  }

  const hasSel = 0 < strip.selectedPoints.length;
  const backGeom = hasSel ? buildStripPaths(strip.allPoints, strip.scaleMin, strip.scaleMax, plotW, height) : null;
  const frontPoints = hasSel ? strip.selectedPoints : strip.allPoints;
  const frontGeom = buildStripPaths(frontPoints, strip.scaleMin, strip.scaleMax, plotW, height);

  const curStr = renderText(strip.currentValueText, locale) || "–";
  const minStr = renderText(strip.minValueText, locale);
  const maxStr = renderText(strip.maxValueText, locale);

  const markCoord = scrubbing
    ? 0 <= scrubIdx && scrubIdx < frontGeom.coords.length
      ? frontGeom.coords[scrubIdx]
      : null
    : 0 <= strip.lastValidIndex && strip.lastValidIndex < frontGeom.coords.length
      ? frontGeom.coords[strip.lastValidIndex]
      : null;

  return (
    <div className="u-strip">
      <div className="u-strip-h">
        <span>{label}</span>
        <b className={strip.hasValue ? undefined : "na"} aria-label={strip.hasValue ? undefined : noSampleA11y}>
          {curStr}
        </b>
        {strip.hasValue && unit && <span>{unit}</span>}
        {strip.showRange && <em>{`${minStr} – ${maxStr}`}</em>}
      </div>
      <svg viewBox={`0 0 ${plotW} ${height}`} height={height} aria-hidden="true">
        {backGeom && backGeom.d && <path className="u-ln back" d={backGeom.d} />}
        {frontGeom.d && <path className="u-ln" d={frontGeom.d} />}
        {frontGeom.dots.map((dot, idx) => (
          <circle key={idx} className="u-dot" cx={dot.x} cy={dot.y} r={1.8} />
        ))}
        {markCoord &&
          (scrubbing ? (
            <circle className="u-ring" cx={markCoord.x} cy={markCoord.y} r={3} />
          ) : (
            <circle className="u-dot" cx={markCoord.x} cy={markCoord.y} r={2.2} />
          ))}
      </svg>
    </div>
  );
}

interface TouchHoldState {
  timer?: ReturnType<typeof setTimeout>;
  pointerId: number | null;
  startX: number;
  startY: number;
  active: boolean;
}

function BarAndStripsSection({
  vm,
  twoCol,
  locale,
  onScrub,
}: {
  vm: UsageViewModel;
  twoCol: boolean;
  locale: Locale;
  onScrub: (bucketIndex: number, clear: boolean) => void;
}) {
  const chartsRef = useRef(null as HTMLDivElement | null);
  const [measuredW, setMeasuredW] = useState(0);
  const touchHoldRef = useRef({ pointerId: null, startX: 0, startY: 0, active: false } as TouchHoldState);

  useEffect(() => {
    const el = chartsRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const measure = () => {
      const w = el.getBoundingClientRect().width;
      if (0 < w) setMeasuredW(Math.round(w));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    return () => {
      if (touchHoldRef.current.timer) clearTimeout(touchHoldRef.current.timer);
    };
  }, []);

  const plotW = 0 < measuredW ? measuredW : twoCol ? 600 : 358;
  const barH = twoCol ? 150 : 108;
  const stripH = twoCol ? 34 : 26;
  const topPad = 12;
  const midY = topPad + (barH - topPad) / 2;

  const barChart = vm.barChart;
  const buckets: readonly UsageBarBucketView[] = barChart?.buckets ?? [];
  const n = buckets.length;
  const hero = vm.hero;
  const scrubbing = !!hero?.scrubbing && 0 < n;
  const scrubIdx = hero?.scrubBucketIndex ?? -1;
  const hasSelected = !!hero?.hasSelectedModel;

  const axisTicks = useMemo(
    () => buildUsageAxisTicks(vm.granularity, buckets, vm.startMs, vm.endMs, vm.preset, locale),
    [vm.granularity, buckets, vm.startMs, vm.endMs, vm.preset, locale],
  );

  const bucketFromClientX = useCallback(
    (clientX: number): number | null => {
      if (n <= 0) return null;
      const el = chartsRef.current;
      if (!el) return null;
      const rect = el.getBoundingClientRect();
      const width = 0 < rect.width ? rect.width : plotW;
      if (width <= 0) return null;
      const idx = Math.floor(((clientX - rect.left) / width) * n);
      return Math.max(0, Math.min(n - 1, idx));
    },
    [n, plotW],
  );

  const clearHoldTimer = useCallback(() => {
    if (touchHoldRef.current.timer) {
      clearTimeout(touchHoldRef.current.timer);
      touchHoldRef.current.timer = undefined;
    }
  }, []);

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    if (n <= 0) return;
    if (e.pointerType === "mouse") {
      clearHoldTimer();
      const b = bucketFromClientX(e.clientX);
      if (b !== null) onScrub(b, false);
      return;
    }
    clearHoldTimer();
    const pid = e.pointerId;
    touchHoldRef.current.pointerId = pid;
    touchHoldRef.current.startX = e.clientX;
    touchHoldRef.current.startY = e.clientY;
    touchHoldRef.current.active = false;
    touchHoldRef.current.timer = setTimeout(() => {
      touchHoldRef.current.timer = undefined;
      touchHoldRef.current.active = true;
      try {
        chartsRef.current?.setPointerCapture(pid);
      } catch {
        // Ignore if pointer capture is unavailable in synthetic tests.
      }
      const b = bucketFromClientX(touchHoldRef.current.startX);
      if (b !== null) onScrub(b, false);
    }, 150);
  };

  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    if (n <= 0) return;
    if (e.pointerType === "mouse") {
      const b = bucketFromClientX(e.clientX);
      if (b !== null && (!scrubbing || b !== scrubIdx)) {
        onScrub(b, false);
      }
      return;
    }
    if (touchHoldRef.current.pointerId !== e.pointerId) return;
    if (!touchHoldRef.current.active) {
      if (Math.hypot(e.clientX - touchHoldRef.current.startX, e.clientY - touchHoldRef.current.startY) > 8) {
        clearHoldTimer();
        touchHoldRef.current.pointerId = null;
      }
      return;
    }
    touchHoldRef.current.startX = e.clientX;
    const b = bucketFromClientX(e.clientX);
    if (b !== null && (!scrubbing || b !== scrubIdx)) {
      onScrub(b, false);
    }
  };

  const endTouch = (e: PointerEvent<HTMLDivElement>) => {
    if (e.pointerType === "mouse") return;
    if (touchHoldRef.current.pointerId !== null && touchHoldRef.current.pointerId !== e.pointerId) return;
    clearHoldTimer();
    const wasActive = touchHoldRef.current.active;
    touchHoldRef.current.active = false;
    touchHoldRef.current.pointerId = null;
    if (wasActive || scrubbing) {
      onScrub(0, true);
    }
  };

  const onPointerLeave = (e: PointerEvent<HTMLDivElement>) => {
    if (e.pointerType === "mouse") {
      if (scrubbing) onScrub(0, true);
    }
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (n <= 0) return;
    if (e.key === "ArrowRight") {
      e.preventDefault();
      onScrub(scrubbing ? Math.min(n - 1, scrubIdx + 1) : n - 1, false);
    } else if (e.key === "ArrowLeft") {
      e.preventDefault();
      onScrub(scrubbing ? Math.max(0, scrubIdx - 1) : 0, false);
    } else if (e.key === "Escape" && scrubbing) {
      e.preventDefault();
      onScrub(0, true);
    }
  };

  const yMax = Number(barChart?.yMax ?? 0n);
  const bw = n > 0 ? plotW / n : 0;
  const gap = n > 26 ? 2 : 3;
  const barInnerH = barH - topPad;
  const a11yLabel = renderText(vm.chartsA11yLabel, locale);
  const noSampleA11y = t("usage.no_sample_a11y", undefined, locale);

  return (
    <div
      ref={chartsRef}
      className="u-charts"
      role="img"
      tabIndex={0}
      aria-label={a11yLabel}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endTouch}
      onPointerCancel={endTouch}
      onLostPointerCapture={endTouch}
      onPointerLeave={onPointerLeave}
      onKeyDown={onKeyDown}
    >
      <svg viewBox={`0 0 ${plotW} ${barH}`} height={barH} aria-hidden="true">
        <line className="u-gl" x1="0" x2={plotW} y1={topPad} y2={topPad} />
        <line className="u-gl" x1="0" x2={plotW} y1={midY} y2={midY} />
        {yMax > 0 &&
          buckets.map((b, i) => {
            const allTok = Number(b.allTokens);
            if (allTok <= 0) return null;
            const x = i * bw + gap / 2;
            const w = Math.max(1, bw - gap);
            const allH = Math.max(1.5, (allTok / yMax) * barInnerH);
            const dim = scrubbing && scrubIdx !== i ? " dim" : "";
            const selTok = Number(b.selectedTokens);
            const selH = selTok > 0 ? Math.max(1.5, (selTok / yMax) * barInnerH) : 0;
            if (hasSelected) {
              return (
                <g key={i}>
                  <rect className={`u-bar ghost${dim}`} x={x} y={barH - allH} width={w} height={allH} rx={1.5} />
                  {selH > 0 && <rect className={`u-bar${dim}`} x={x} y={barH - selH} width={w} height={selH} rx={1.5} />}
                </g>
              );
            }
            return <rect key={i} className={`u-bar${dim}`} x={x} y={barH - allH} width={w} height={allH} rx={1.5} />;
          })}
        {yMax > 0 && (
          <>
            <text className="u-yl" x={plotW} y={topPad - 4} textAnchor="end">
              {renderText(barChart?.yMaxText, locale)}
            </text>
            <text className="u-yl" x={plotW} y={midY - 4} textAnchor="end">
              {renderText(barChart?.yMidText, locale)}
            </text>
          </>
        )}
        <line className="u-base" x1="0" x2={plotW} y1={barH - 0.5} y2={barH - 0.5} />
      </svg>

      <MetricStrip
        label={t("usage.strip_cache_hit", undefined, locale)}
        strip={vm.cacheHitStrip}
        locale={locale}
        scrubbing={scrubbing}
        scrubIdx={scrubIdx}
        plotW={plotW}
        height={stripH}
        noSampleA11y={noSampleA11y}
      />
      <MetricStrip
        label={t("usage.strip_speed", undefined, locale)}
        unit={t("usage.kpi_unit_toks", undefined, locale)}
        strip={vm.outputSpeedStrip}
        locale={locale}
        scrubbing={scrubbing}
        scrubIdx={scrubIdx}
        plotW={plotW}
        height={stripH}
        noSampleA11y={noSampleA11y}
      />
      <MetricStrip
        label={t("usage.strip_ttft", undefined, locale)}
        strip={vm.ttftStrip}
        locale={locale}
        scrubbing={scrubbing}
        scrubIdx={scrubIdx}
        plotW={plotW}
        height={stripH}
        noSampleA11y={noSampleA11y}
      />

      <div className="u-axis">
        {axisTicks.map((tk, idx) => (
          <span
            key={idx}
            className={tk.align || undefined}
            style={{ left: `${tk.pct.toFixed(2)}%` }}
          >
            {tk.label}
          </span>
        ))}
      </div>

      {scrubbing && n > 0 && (
        <div className="u-cursor" style={{ left: `${(((scrubIdx + 0.5) / n) * 100).toFixed(3)}%` }} aria-hidden="true" />
      )}
    </div>
  );
}

function ModelTableSection({
  vm,
  twoCol,
  locale,
  onSelectSort,
  onRowClick,
}: {
  vm: UsageViewModel;
  twoCol: boolean;
  locale: Locale;
  onSelectSort: (col: UsageSortColumn) => void;
  onRowClick: (row: UsageModelRowView) => void;
}) {
  const sortBy = vm.sortBy === UsageSortColumn.UNSPECIFIED ? UsageSortColumn.TOKENS : vm.sortBy;
  const cols: Array<{ col: UsageSortColumn; label: string; asc?: boolean }> = [
    { col: UsageSortColumn.TOKENS, label: t("usage.col_tokens", undefined, locale) },
    { col: UsageSortColumn.CACHE_HIT, label: t("usage.col_cache", undefined, locale) },
    { col: UsageSortColumn.SPEED, label: t("usage.col_speed", undefined, locale) },
    { col: UsageSortColumn.TTFT, label: t("usage.col_ttft", undefined, locale), asc: true },
  ];
  const noSampleA11y = t("usage.no_sample_a11y", undefined, locale);
  const nameMax = twoCol ? 42 : 36;
  const showExtra = !!vm.unsupportedFootnote;

  return (
    <div className="u-table">
      <div className="u-cols u-thead">
        <span className="u-th-model">{t("usage.col_model", undefined, locale)}</span>
        {cols.map((c) => {
          const active = sortBy === c.col;
          const arrow = active ? (c.asc ? " ↑" : " ↓") : "";
          return (
            <button
              key={c.col}
              type="button"
              className={active ? "on" : undefined}
              aria-pressed={active}
              onClick={() => onSelectSort(c.col)}
            >
              {`${c.label}${arrow}`}
            </button>
          );
        })}
      </div>

      {vm.rows.map((row) => {
        const deltaStr = renderText(row.deltaText, locale);
        return (
          <button
            key={row.seriesKey}
            type="button"
            className={row.selected ? "u-trow sel" : "u-trow"}
            aria-pressed={row.selected}
            onClick={() => onRowClick(row)}
          >
            <div className="u-l1">
              <AgentMark agent={row.agent} />
              <span className="nm" title={row.model}>
                {middleEllipsis(row.model, nameMax)}
              </span>
              {deltaStr && <span className="dl">{deltaStr}</span>}
            </div>
            <div className="u-cols u-l2">
              <div className="u-sh" aria-hidden="true">
                <i style={{ width: `${Math.max(0, Math.min(100, row.shareRatio * 100)).toFixed(2)}%` }} />
              </div>
              <span className="tk">{renderText(row.totalTokensText, locale)}</span>
              <span className={row.cacheHitText ? undefined : "na"} aria-label={row.cacheHitText ? undefined : noSampleA11y}>
                {renderText(row.cacheHitText, locale) || "–"}
              </span>
              <span className={row.speedText ? undefined : "na"} aria-label={row.speedText ? undefined : noSampleA11y}>
                {renderText(row.speedText, locale) || "–"}
              </span>
              <span className={row.ttftText ? undefined : "na"} aria-label={row.ttftText ? undefined : noSampleA11y}>
                {renderText(row.ttftText, locale) || "–"}
              </span>
            </div>
          </button>
        );
      })}

      <div className="u-foot">
        <p>{t("usage.footer_note", undefined, locale)}</p>
        {showExtra && <p>{renderText(vm.unsupportedFootnote)}</p>}
      </div>
    </div>
  );
}

function RangeSegmented({
  preset,
  locale,
  options,
  onSelectPreset,
  onClearEditing,
}: {
  preset: UsageRangePreset;
  locale: Locale;
  options: Array<{ value: string; label: string }>;
  onSelectPreset: (preset: UsageRangePreset) => void;
  onClearEditing: () => void;
}) {
  return (
    <Segmented
      className="u-seg"
      label={t("usage.title", undefined, locale)}
      value={String(preset)}
      options={options}
      onChange={(next) => {
        const p = Number(next) as UsageRangePreset;
        if (p !== UsageRangePreset.CUSTOM) onClearEditing();
        onSelectPreset(p);
      }}
    />
  );
}

function CustomRangeButton({
  startMs,
  endMs,
  locale,
  expanded,
  onToggle,
}: {
  startMs: bigint;
  endMs: bigint;
  locale: Locale;
  expanded: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      className="u-range"
      aria-expanded={expanded}
      onClick={onToggle}
    >
      <span>{formatUsageCustomRange(startMs, endMs, locale)}</span>
      <ChevronDownSmall />
    </button>
  );
}

function CustomRangePicker({
  startMs,
  endMs,
  minDateStr,
  todayDateStr,
  locale,
  onSelectCustomRange,
}: {
  startMs: bigint;
  endMs: bigint;
  minDateStr: string;
  todayDateStr: string;
  locale: Locale;
  onSelectCustomRange: (startMs: bigint, endMs: bigint) => void;
}) {
  return (
    <div className="u-range-pick">
      <div className="u-range-fields">
        <label>
          <span>{t("usage.custom_start_label", undefined, locale)}</span>
          <input
            type="date"
            min={minDateStr}
            max={todayDateStr}
            value={toLocalDateInputValue(startMs)}
            onChange={(e) => {
              const nextStart = fromLocalDateInputValue(e.target.value);
              if (nextStart === undefined) return;
              const next = clampUsageCustomRange(nextStart, endMs < nextStart ? nextStart : endMs);
              onSelectCustomRange(next.startMs, next.endMs);
            }}
          />
        </label>
        <label>
          <span>{t("usage.custom_end_label", undefined, locale)}</span>
          <input
            type="date"
            min={minDateStr}
            max={todayDateStr}
            value={toLocalDateInputValue(endMs)}
            onChange={(e) => {
              const nextEnd = fromLocalDateInputValue(e.target.value);
              if (nextEnd === undefined) return;
              const next = clampUsageCustomRange(nextEnd < startMs ? nextEnd : startMs, nextEnd);
              onSelectCustomRange(next.startMs, next.endMs);
            }}
          />
        </label>
      </div>
      <div className="u-range-hint">{t("usage.custom_range_limit_hint", undefined, locale)}</div>
    </div>
  );
}

export function UsageScreenView({
  vm,
  wide = false,
  onBack,
  onSelectPreset,
  onSelectCustomRange,
  onSelectSort,
  onSelectSeries,
  onScrub,
  onRetry,
}: {
  vm: UsageViewModel;
  wide?: boolean;
  onBack: () => void;
  onSelectPreset: (preset: UsageRangePreset) => void;
  onSelectCustomRange: (startMs: bigint, endMs: bigint) => void;
  onSelectSort: (sortBy: UsageSortColumn) => void;
  onSelectSeries: (seriesKey: string, toggle: boolean) => void;
  onScrub: (bucketIndex: number, clear: boolean) => void;
  onRetry: () => void;
}) {
  const locale = useLocale();
  const largeFont = useLargeFontScale();
  const pageRef = useRef(null as HTMLDivElement | null);
  const bodyRef = useRef(null as HTMLDivElement | null);
  const [contentWidth, setContentWidth] = useState(0);
  const [editingRange, setEditingRange] = useState(false);
  const [showSkeleton, setShowSkeleton] = useState(false);

  useEffect(() => {
    const el = pageRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const measure = () => {
      const w = el.getBoundingClientRect().width;
      if (0 < w) setContentWidth(Math.round(w));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const twoCol = 0 < contentWidth ? 900 <= contentWidth : wide;

  useEffect(() => {
    if (!vm.loading) {
      setShowSkeleton(false);
      return;
    }
    const timer = setTimeout(() => setShowSkeleton(true), 200);
    return () => clearTimeout(timer);
  }, [vm.loading]);

  const preset = !vm.preset ? UsageRangePreset.SEVEN_DAYS : vm.preset;
  const clampedRange = useMemo(() => {
    const endInc = vm.startMs < vm.endMs && 0n < vm.endMs ? vm.endMs - 1n : vm.endMs;
    return clampUsageCustomRange(vm.startMs, endInc);
  }, [vm.startMs, vm.endMs]);

  const subtitle = renderText(vm.subtitle, locale) || vm.machineName || vm.machineId;
  const isEmptyCard =
    vm.emptyKind === UsageEmptyKind.NOT_FOUND ||
    vm.emptyKind === UsageEmptyKind.UNSUPPORTED ||
    vm.emptyKind === UsageEmptyKind.OFFLINE_EMPTY ||
    vm.emptyKind === UsageEmptyKind.LOAD_ERROR ||
    vm.emptyKind === UsageEmptyKind.ALL_SOURCES_UNAVAILABLE;

  const minDateStr = useMemo(() => toLocalDateInputValue(clampUsageCustomRange(1n, 1n).startMs), []);
  const todayDateStr = useMemo(() => toLocalDateInputValue(Date.now()), []);

  const rangeOptions = RANGE_OPTIONS.map((o) => ({ value: o.value, label: o.label(locale) }));

  const handleRowClick = (row: UsageModelRowView) => {
    const selecting = !row.selected;
    onSelectSeries(row.seriesKey, true);
    if (!twoCol && selecting) {
      bodyRef.current?.scrollTo?.({ top: 0, behavior: "smooth" });
    }
  };

  const emptyAxisTicks = useMemo(
    () => {
      return buildUsageAxisTicks(vm.granularity, vm.barChart?.buckets ?? [], vm.startMs, vm.endMs, preset, locale);
    },
    [vm.granularity, vm.barChart?.buckets, vm.startMs, vm.endMs, preset, locale],
  );

  return (
    <div
      ref={pageRef}
      className={twoCol ? "page usage-page u-two" : "page usage-page"}
      data-u-zoom={largeFont ? "200" : undefined}
    >
      <NavBar
        onBack={onBack}
        title={t("usage.title", undefined, locale)}
        sub={subtitle}
        right={
          twoCol ? (
            <div className="u-nav-right">
              {preset === UsageRangePreset.CUSTOM && (
                <CustomRangeButton
                  startMs={clampedRange.startMs}
                  endMs={clampedRange.endMs}
                  locale={locale}
                  expanded={editingRange}
                  onToggle={() => setEditingRange((v) => !v)}
                />
              )}
              <RangeSegmented
                preset={preset}
                locale={locale}
                options={rangeOptions}
                onSelectPreset={onSelectPreset}
                onClearEditing={() => setEditingRange(false)}
              />
            </div>
          ) : undefined
        }
      />

      <div ref={bodyRef} className={twoCol ? "body u-body u-two" : "body u-body"}>
        {!twoCol && (
          <div className="u-in">
            <RangeSegmented
              preset={preset}
              locale={locale}
              options={rangeOptions}
              onSelectPreset={onSelectPreset}
              onClearEditing={() => setEditingRange(false)}
            />
            {preset === UsageRangePreset.CUSTOM && (
              <div className="u-range-wrap">
                <CustomRangeButton
                  startMs={clampedRange.startMs}
                  endMs={clampedRange.endMs}
                  locale={locale}
                  expanded={editingRange}
                  onToggle={() => setEditingRange((v) => !v)}
                />
              </div>
            )}
          </div>
        )}

        {preset === UsageRangePreset.CUSTOM && editingRange && (
          <div className={twoCol ? undefined : "u-in"}>
            <CustomRangePicker
              startMs={clampedRange.startMs}
              endMs={clampedRange.endMs}
              minDateStr={minDateStr}
              todayDateStr={todayDateStr}
              locale={locale}
              onSelectCustomRange={onSelectCustomRange}
            />
          </div>
        )}

        {vm.banners.length > 0 && (
          <div className={twoCol ? "u-banners" : "u-in u-banners"}>
            {vm.banners.map((b, idx) => (
              <Banner
                key={`${b.kind}-${idx}`}
                kind={b.isError ? "err" : undefined}
                action={b.canRetry ? t("common.retry", undefined, locale) : undefined}
                onAction={b.canRetry ? onRetry : undefined}
              >
                {renderText(b.text, locale)}
              </Banner>
            ))}
          </div>
        )}

        {vm.emptyKind === UsageEmptyKind.LOADING && showSkeleton && (
          <div className="u-in">
            <div className="u-skel" aria-busy="true" />
          </div>
        )}

        {isEmptyCard && (
          <div className="u-in">
            <div className="u-empty">
              <b>{renderText(vm.emptyTitle, locale)}</b>
              {vm.emptyBody && <p>{renderText(vm.emptyBody, locale)}</p>}
              {vm.emptyCanRetry && (
                <button type="button" className="btn pri" onClick={onRetry}>
                  {t("common.retry", undefined, locale)}
                </button>
              )}
            </div>
          </div>
        )}

        {vm.emptyKind === UsageEmptyKind.NO_RECORDS && (
          <div className={twoCol ? "u-wbody" : "u-in"}>
            <div className={twoCol ? "u-wl" : undefined}>
              <HeroSection vm={vm} twoCol={twoCol} locale={locale} onClearSeries={() => onSelectSeries("", false)} />
              <div className="u-charts is-empty">
                <div className="u-nodata">{renderText(vm.emptyTitle, locale) || t("usage.empty_no_records", undefined, locale)}</div>
                <div className="u-axis">
                  {emptyAxisTicks.map((tk, idx) => (
                    <span
                      key={idx}
                      className={tk.align || undefined}
                      style={{ left: `${tk.pct.toFixed(2)}%` }}
                    >
                      {tk.label}
                    </span>
                  ))}
                </div>
              </div>
            </div>
          </div>
        )}

        {vm.emptyKind === UsageEmptyKind.NONE && vm.hasAnyUsage && (
          <div className={twoCol ? "u-wbody" : "u-in"}>
            <div className={twoCol ? "u-wl" : undefined}>
              <HeroSection
                vm={vm}
                twoCol={twoCol}
                locale={locale}
                onClearSeries={() => onSelectSeries("", false)}
              />
              <BarAndStripsSection vm={vm} twoCol={twoCol} locale={locale} onScrub={onScrub} />
            </div>
            <div className={twoCol ? "u-wr" : undefined}>
              <ModelTableSection
                vm={vm}
                twoCol={twoCol}
                locale={locale}
                onSelectSort={onSelectSort}
                onRowClick={handleRowClick}
              />
            </div>
          </div>
        )}
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
  const host = useCoreHost(remote, go);
  const [preset, setPreset] = useState(UsageRangePreset.SEVEN_DAYS as UsageRangePreset);
  const [customRange, setCustomRange] = useState(() => clampUsageCustomRange(0n, 0n));
  const vm = useUsageView(host, {
    machineId,
    focusAgent,
    focusModel,
    preset,
    customStartMs: preset === UsageRangePreset.CUSTOM ? customRange.startMs : 0n,
    customEndMs: preset === UsageRangePreset.CUSTOM ? customRange.endMs : 0n,
  });

  return (
    <UsageScreenView
      vm={vm}
      wide={wide}
      onBack={onBack}
      onSelectPreset={setPreset}
      onSelectCustomRange={(startMs, endMs) => {
        setCustomRange({ startMs, endMs });
        setPreset(UsageRangePreset.CUSTOM);
      }}
      onSelectSort={(col) => host.selectUsageSort(machineId, col)}
      onSelectSeries={(seriesKey, toggle) => host.selectUsageSeries(machineId, seriesKey, toggle)}
      onScrub={(idx, clear) => host.scrubUsage(machineId, idx, clear)}
      onRetry={() => host.retryUsageScreen(machineId)}
    />
  );
}

export { routeForUsage };
