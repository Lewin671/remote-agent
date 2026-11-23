// One computer, watched in two-second ticks while the page is in front
// (client-v28): CPU %, memory, swap and pressure, disk and what remote-agent
// uses of it, and what runs inside remote-agent and across the computer.
// Core (clientcore) owns polling, scrub selection, formatting, and rankings;
// this file renders the MachineStatusViewModel and SVG curve geometry.

import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from "react";
import { t } from "../i18n";
import { AgentMark } from "../agent-mark";
import {
  MachineChartKind,
  MachinePollMode,
  MachinePollState,
  MachinePressureTone,
  DiskPressureState,
  MachineStatusDot,
  MachineUsageTab,
  type MachineChartPoint,
  type MachineChartView,
  type MachineStatusViewModel,
} from "../gen/remoteagent/core/v1/machine_view_pb";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { CpuIcon, DiskIcon, MemoryIcon, TerminalIcon, ChevronRight } from "../icons";
import {
  routeForProject,
  routeForUsage,
  useCoreHost,
  useMachineStatusView,
} from "../core/host";
import { renderText } from "../core/text";
import type { Remote } from "../remote";
import { NavBar } from "../ui";
import { addProject, MachineDirSheet as NewProjectSheet } from "./ScheduleConfig";
import { Roll, flipBox, useFlipList, usePageVisible, useReducedMotion } from "./machine-status-hooks";

type Go = (route: string) => void;
type Main = "cpu" | "mem";

const HEIGHT = 104, DOT_INSET = 4, PAD = 4, DEFAULT_WINDOW_MS = 120_000, DEFAULT_GAP_MS = 10_000;
type Pt = { x: number; y: number };
const PRESSURE_TONE: Record<number, "" | "warn" | "crit"> = { [MachinePressureTone.HIGH]: "warn", [MachinePressureTone.CRITICAL]: "crit" };
const DISK_TONE: Record<number, "" | "warn" | "crit"> = { [DiskPressureState.LOW]: "warn", [DiskPressureState.CRITICAL]: "crit" };
const DOT_CLS: Record<number, string> = { [MachineStatusDot.GREEN_LIVE]: "ms-dot", [MachineStatusDot.ORANGE_STATIC]: "ms-dot off", [MachineStatusDot.RED_STATIC]: "ms-dot off" };

class ChartTimeline {
  private end?: number; private latest = 0; private lastNow = 0;
  private catchup?: { from: number; target: number; start: number; deadline: number };
  advance(nowMs: number, firstMs: number, latestMs: number, moving = true): number {
    if (latestMs <= 0) return 0;
    if (this.end === undefined || !moving) {
      this.end = moving ? Math.max(firstMs, latestMs - 2000) : latestMs;
      this.latest = latestMs; this.lastNow = nowMs; this.catchup = undefined;
      return this.end;
    }
    const now = Math.max(this.lastNow, nowMs), c = this.catchup;
    if (c) {
      const p = Math.min(1, (now - c.start) / (c.deadline - c.start));
      this.end = c.from + (c.target - c.from) * (1 - (1 - p) ** 3);
      if (p === 1) this.catchup = undefined;
    } else this.end = Math.min(this.latest, this.end + now - this.lastNow);
    this.lastNow = now;
    if (latestMs > this.latest) {
      this.latest = latestMs;
      if (this.catchup || latestMs - this.end > 2000) this.catchup = { from: this.end, target: latestMs, start: now, deadline: this.catchup?.deadline ?? now + 500 };
    }
    return this.end;
  }
}

const xOf = (t: number, at: number, width: number, windowMs: number) => (width - DOT_INSET) * (1 - (at - t) / windowMs);
const yOf = (v: number, max: number, height: number) => PAD + (1 - Math.min(1, Math.max(0, max > 0 ? v / max : 0))) * (height - 2 * PAD);

function smooth(pts: readonly Pt[]): string {
  const n = pts.length;
  if (n === 1) return `M${pts[0].x.toFixed(2)},${pts[0].y.toFixed(2)}`;
  const dx = pts.slice(0, -1).map((p, i) => pts[i + 1].x - p.x);
  const slope = dx.map((w, i) => (w === 0 ? 0 : (pts[i + 1].y - pts[i].y) / w));
  const tan = [slope[0], ...slope.slice(1).map((s, i) => (slope[i] * s <= 0 ? 0 : (slope[i] + s) / 2)), slope[n - 2]];
  for (let i = 0; i < n - 1; i++) {
    if (slope[i] === 0) { tan[i] = tan[i + 1] = 0; continue; }
    const a = tan[i] / slope[i], b = tan[i + 1] / slope[i], h = Math.hypot(a, b);
    if (h > 3) { tan[i] = (3 * a * slope[i]) / h; tan[i + 1] = (3 * b * slope[i]) / h; }
  }
  return `M${pts[0].x.toFixed(2)},${pts[0].y.toFixed(2)}` + dx.map((w, i) => {
    const k = w / 3;
    return ` C${(pts[i].x + k).toFixed(2)},${(pts[i].y + k * tan[i]).toFixed(2)} ${(pts[i + 1].x - k).toFixed(2)},${(pts[i + 1].y - k * tan[i + 1]).toFixed(2)} ${pts[i + 1].x.toFixed(2)},${pts[i + 1].y.toFixed(2)}`;
  }).join("");
}

function curvePaths(points: readonly { t: number; v: number }[], at: number, max: number, width: number, height: number, windowMs: number, gapMs: number) {
  const shown: { t: number; v: number }[] = [];
  for (let i = 0; i < points.length; i++) {
    const p = points[i], prev = points[i - 1];
    if (p.t <= at) shown.push(p);
    else { if (prev && p.t - prev.t <= gapMs && at > prev.t) shown.push({ t: at, v: prev.v + ((p.v - prev.v) * (at - prev.t)) / (p.t - prev.t) }); break; }
  }
  const clipped = shown.filter((p) => p.t >= at - windowMs - gapMs);
  const runs: Pt[][] = [];
  clipped.forEach((p, idx) => {
    const pt = { x: xOf(p.t, at, width, windowMs), y: yOf(p.v, max, height) };
    if (idx === 0 || p.t - clipped[idx - 1].t > gapMs) runs.push([pt]); else runs.at(-1)!.push(pt);
  });
  let line = "", area = "";
  for (const run of runs) {
    if (run.length < 2) continue;
    const d = smooth(run);
    line += (line ? " " : "") + d;
    area += (area ? " " : "") + `${d} L${run.at(-1)!.x.toFixed(2)},${height} L${run[0].x.toFixed(2)},${height} Z`;
  }
  const last = clipped.at(-1);
  return { line, area, head: last ? { x: xOf(last.t, at, width, windowMs), y: yOf(last.v, max, height) } : undefined };
}

function sampleIndexAt(points: readonly MachineChartPoint[], x: number, at: number, width: number, windowMs: number, gapMs: number): number {
  const target = at - (1 - x / (width - DOT_INSET)) * windowMs;
  let best = -1, bestDist = Infinity;
  points.forEach((pt, i) => {
    const ms = Number(pt.timeMs);
    const dist = ms <= at + 1 ? Math.abs(ms - target) : Infinity;
    if (dist < bestDist) { bestDist = dist; best = i; }
  });
  return best >= 0 && bestDist <= gapMs / 2 ? best : -1;
}

function Curve({ chart, tone, stale, reduced, moving, onScrub, onClearScrub }: {
  chart: MachineChartView; tone: "" | "warn" | "crit"; stale: boolean; reduced: boolean; moving: boolean;
  onScrub: (index: number) => void; onClearScrub: () => void;
}) {
  const plot = useRef<HTMLDivElement>(null), areaRef = useRef<SVGPathElement>(null), lineRef = useRef<SVGPathElement>(null);
  const dotRef = useRef<SVGCircleElement>(null), ruleRef = useRef<SVGLineElement>(null), spotRef = useRef<SVGCircleElement>(null);
  const [width, setWidth] = useState(358);
  const gid = useId();
  useLayoutEffect(() => {
    const el = plot.current; if (!el) return;
    const measure = () => { if (el.clientWidth > 0) setWidth(Math.round(el.clientWidth)); };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const obs = new ResizeObserver(measure); obs.observe(el); return () => obs.disconnect();
  }, []);

  const timeline = useRef(new ChartTimeline());
  const rawPoints = chart.points;
  const pts = useMemo(() => rawPoints.map((p) => ({ t: Number(p.timeMs), v: p.value })), [rawPoints]);
  const windowMs = Math.max(1000, Number(chart.windowEndMs - chart.windowStartMs) || DEFAULT_WINDOW_MS);
  const gapMs = Math.max(1000, Number(chart.gapThresholdMs) || DEFAULT_GAP_MS);
  const max = Math.max(1, chart.ceiling || 100);
  const firstMs = pts[0]?.t ?? Number(chart.windowStartMs), latestMs = pts.at(-1)?.t ?? Number(chart.windowEndMs);
  const live = useRef({ pts, rawPoints, firstMs, latestMs, max, width, windowMs, gapMs, moving: false, hasScrub: false, scrubIndex: 0, at: latestMs });
  Object.assign(live.current, { pts, rawPoints, firstMs, latestMs, max, width, windowMs, gapMs, moving: moving && !reduced && !chart.hasScrub, hasScrub: chart.hasScrub, scrubIndex: chart.scrubIndex });

  const draw = useCallback(() => {
    const s = live.current, at = timeline.current.advance(performance.now(), s.firstMs, s.latestMs, s.moving);
    s.at = at;
    const { line, area, head } = curvePaths(s.pts, at, s.max, s.width, HEIGHT, s.windowMs, s.gapMs);
    lineRef.current?.setAttribute("d", line); areaRef.current?.setAttribute("d", area);
    dotRef.current?.setAttribute("visibility", head && !s.hasScrub ? "visible" : "hidden");
    if (head && dotRef.current) { dotRef.current.setAttribute("cx", String(head.x)); dotRef.current.setAttribute("cy", String(head.y)); }
    const scrubPt = s.hasScrub ? s.rawPoints[s.scrubIndex] : undefined;
    ruleRef.current?.setAttribute("visibility", scrubPt ? "visible" : "hidden");
    spotRef.current?.setAttribute("visibility", scrubPt ? "visible" : "hidden");
    if (scrubPt) {
      const sx = xOf(Number(scrubPt.timeMs), at, s.width, s.windowMs), sy = yOf(scrubPt.value, s.max, HEIGHT);
      ruleRef.current?.setAttribute("x1", String(sx)); ruleRef.current?.setAttribute("x2", String(sx));
      spotRef.current?.setAttribute("cx", String(sx)); spotRef.current?.setAttribute("cy", String(sy));
    }
  }, []);

  useLayoutEffect(() => {
    draw();
    if (!moving || reduced || chart.hasScrub || typeof requestAnimationFrame !== "function") return;
    let raf = 0; const loop = () => { draw(); raf = requestAnimationFrame(loop); };
    raf = requestAnimationFrame(loop); return () => cancelAnimationFrame(raf);
  }, [draw, pts, width, max, windowMs, gapMs, moving, reduced, chart.hasScrub, chart.scrubIndex]);

  const fill = tone === "crit" ? "var(--red)" : tone === "warn" ? "var(--orange)" : "var(--ink)";
  const lineColour = stale ? "var(--ink3)" : fill;
  const stepSample = (dir: -1 | 1) => {
    if (!rawPoints.length) return;
    if (dir < 0) { const next = chart.hasScrub ? chart.scrubIndex - 1 : Math.max(0, rawPoints.length - 2); if (next >= 0) onScrub(next); }
    else if (chart.hasScrub) { if (chart.scrubIndex >= rawPoints.length - 2) onClearScrub(); else onScrub(chart.scrubIndex + 1); }
  };
  const onPointer = (e: PointerEvent<HTMLDivElement>) => {
    if (e.pointerType === "mouse" && e.type === "pointerdown") return;
    if (e.type === "pointerdown") e.currentTarget.setPointerCapture?.(e.pointerId);
    const r = plot.current?.getBoundingClientRect(); if (!r) return;
    const idx = sampleIndexAt(rawPoints, (Math.max(0, Math.min(r.width, e.clientX - r.left)) / (r.width || 1)) * width, live.current.at, width, windowMs, gapMs);
    if (idx >= 0) onScrub(idx); else onClearScrub();
  };
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "ArrowLeft") { stepSample(-1); e.preventDefault(); }
    else if (e.key === "ArrowRight" && chart.hasScrub) { stepSample(1); e.preventDefault(); }
    else if (e.key === "Escape" && chart.hasScrub) { onClearScrub(); e.preventDefault(); }
  };
  return (
    <div className="ms-plot" ref={plot} role="slider" tabIndex={0} aria-valuemin={0} aria-valuemax={Math.max(0, rawPoints.length - 1)}
      aria-valuenow={chart.hasScrub ? chart.scrubIndex : Math.max(0, rawPoints.length - 1)}
      aria-valuetext={chart.hasScrub ? renderText(chart.scrubA11yValue) : undefined}
      aria-label={renderText(chart.a11ySummary)} onPointerDown={onPointer} onPointerMove={onPointer}
      onPointerUp={onClearScrub} onPointerCancel={onClearScrub} onPointerLeave={onClearScrub} onKeyDown={onKey}>
      <svg viewBox={`0 0 ${width} ${HEIGHT}`} preserveAspectRatio="none" aria-hidden="true">
        <defs><linearGradient id={gid} x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor={lineColour} stopOpacity={stale ? 0.1 : 0.2} /><stop offset="100%" stopColor={lineColour} stopOpacity={0} /></linearGradient></defs>
        <line className="ms-g" x1="0" y1={HEIGHT / 2} x2={width} y2={HEIGHT / 2} />
        <line className="ms-g" x1="0" y1={HEIGHT - 1} x2={width} y2={HEIGHT - 1} />
        <path ref={areaRef} fill={`url(#${gid})`} />
        <path ref={lineRef} fill="none" stroke={lineColour} strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" />
        <line ref={ruleRef} className="ms-ch" y1="0" y2={HEIGHT} visibility="hidden" />
        <circle ref={dotRef} r="4" fill={lineColour} stroke="var(--gbg)" strokeWidth="2.5" visibility="hidden" />
        <circle ref={spotRef} r="5" fill={lineColour} stroke="var(--gbg)" strokeWidth="2.5" visibility="hidden" />
      </svg>
      <button type="button" className="ms-sr" disabled={!rawPoints.length || (chart.hasScrub && chart.scrubIndex <= 0)} onClick={() => stepSample(-1)}>{t("machine_status.a11y_prev_sample")}</button>
      <button type="button" className="ms-sr" disabled={!chart.hasScrub} onClick={() => stepSample(1)}>{t("machine_status.a11y_next_sample")}</button>
    </div>
  );
}

const usageIcon = (agent: Agent) => (
  <span className={agent !== Agent.UNSPECIFIED ? "ms-ic" : "ms-ic pr"} aria-hidden="true">
    {agent !== Agent.UNSPECIFIED ? <AgentMark agent={agent} /> : <TerminalIcon />}
  </span>
);

function Hero({ vm, main, moving, reduced, onScrub, onClearScrub }: {
  vm: MachineStatusViewModel; main: Main; moving: boolean; reduced: boolean;
  onScrub: (kind: MachineChartKind, index: number) => void; onClearScrub: (kind: MachineChartKind) => void;
}) {
  const cpu = vm.cpu, mem = vm.memory, isCpu = main === "cpu", chart = isCpu ? cpu?.chart : mem?.chart;
  const chartKind = isCpu ? MachineChartKind.CPU : MachineChartKind.MEMORY;
  const tone = isCpu ? "" : (PRESSURE_TONE[mem?.pressure ?? 0] ?? "");
  let headMain = "—", headUnit = "";
  if (chart?.hasScrub) {
    headMain = chart.scrubValueText || "—";
    headUnit = chart.scrubUnit || "";
  } else if (isCpu) {
    headMain = cpu?.valueText || "—";
    headUnit = cpu?.unit || "";
  } else {
    headMain = mem?.valueText || "—";
    if (mem?.hasValue) headUnit = renderText(mem.caption);
  }
  const aside1 = isCpu ? renderText(cpu?.detail2) : renderText(mem?.totalText), aside2 = isCpu ? renderText(cpu?.detail) : "";
  return (
    <section className="ms-card ms-hero">
      <div className="ms-top">
        <div>
          <div className="ms-kick">{isCpu ? <CpuIcon /> : <MemoryIcon />}{isCpu ? t("machine_status.cpu") : t("machine_status.memory")}</div>
          <div className="ms-xl" aria-label={renderText(isCpu ? cpu?.a11yLabel : mem?.a11yLabel)}>
            <Roll text={headMain} />
            {isCpu ? (headUnit && <sup>{headUnit}</sup>) : (headUnit && <small>{headUnit}</small>)}
          </div>
        </div>
        {(aside1 || aside2) && <div className="ms-aside">{aside1 && <div><b>{aside1}</b></div>}{aside2 && <div>{aside2}</div>}</div>}
      </div>
      {!isCpu && renderText(mem?.pressureText) && <div className="ms-pr"><b className={tone}>{renderText(mem?.pressureText)}</b></div>}
      {chart?.visible && (
        <>
          <Curve chart={chart} tone={tone} stale={vm.pollState === MachinePollState.STALE} reduced={reduced} moving={moving}
            onScrub={(idx) => onScrub(chartKind, idx)} onClearScrub={() => onClearScrub(chartKind)} />
          <div className="ms-axis"><span>{t("machine_status.axis_2min")}</span><span>{t("machine_status.axis_1min")}</span><span>{t("machine_status.axis_now")}</span></div>
        </>
      )}
    </section>
  );
}

function MemTile({ vm, onOpen }: { vm: MachineStatusViewModel; onOpen: () => void }) {
  const mem = vm.memory, tone = PRESSURE_TONE[mem?.pressure ?? 0] ?? "";
  return (
    <button type="button" className="ms-card ms-tile" aria-label={renderText(mem?.a11yLabel) || t("machine_status.memory")} onClick={onOpen}>
      <span className="ms-kick"><MemoryIcon />{t("machine_status.memory")}</span>
      <span className="ms-lg"><Roll text={mem?.valueText || "—"} />{mem?.hasValue && <small>{renderText(mem.caption)}</small>}</span>
      <span className="ms-say"><b className={tone}>{renderText(mem?.pressureText)}</b>{renderText(mem?.totalText)}</span>
      {mem?.hasPressurePosition && <span className="ms-uv" aria-hidden="true"><i style={{ left: `${Math.min(100, Math.max(0, mem.pressurePosition * 100)).toFixed(1)}%` }} /></span>}
    </button>
  );
}

function CpuTile({ vm, onOpen }: { vm: MachineStatusViewModel; onOpen: () => void }) {
  const cpu = vm.cpu, d2 = renderText(cpu?.detail2), d = renderText(cpu?.detail);
  return (
    <button type="button" className="ms-card ms-tile" aria-label={renderText(cpu?.a11yLabel) || t("machine_status.cpu")} onClick={onOpen}>
      <span className="ms-kick"><CpuIcon />{t("machine_status.cpu")}</span>
      <span className="ms-lg"><Roll text={cpu?.hasValue ? `${cpu.valueText}${cpu.unit}` : "—"} /></span>
      <span className="ms-say">{d2 && <b>{d2}</b>}{d}</span>
    </button>
  );
}

function DiskTile({ vm, onOpen }: { vm: MachineStatusViewModel; onOpen: () => void }) {
  const disk = vm.disk; if (!disk) return null;
  const tone = DISK_TONE[disk.state] ?? "", usedPct = disk.hasUsedFraction ? Math.min(100, Math.max(0, Math.round(disk.usedFraction * 100))) : 0, d2 = renderText(disk.detail2);
  return (
    <button type="button" className="ms-card ms-tile" aria-label={renderText(disk.a11yLabel) || t("machine_status.disk")} onClick={onOpen}>
      <span className="ms-kick"><DiskIcon />{t("machine_status.disk")}</span>
      <span className={`ms-lg${tone ? ` ${tone}` : ""}`}><Roll text={disk.valueText || "—"} />{disk.hasValue && <small>{renderText(disk.caption) || disk.unit}</small>}</span>
      <span className="ms-say"><b className={tone}>{renderText(disk.stateBadge)}</b>{renderText(disk.detail)}{d2 && <><br />{d2}</>}</span>
      {disk.hasUsedFraction && <span className={`ms-stor${tone ? ` ${tone}` : ""}`} aria-hidden="true">{usedPct > 0 && <i className="a" style={{ width: `${usedPct}%` }} />}</span>}
    </button>
  );
}

function Ranking({ vm, stale, main, go }: { vm: MachineStatusViewModel; stale: boolean; main: Main; go: Go }) {
  const list = useRef<HTMLUListElement>(null);
  useFlipList(list, useReducedMotion() ? 0 : 300);
  const top = vm.topUsage;
  if (!top || !top.rows.length) return <div className="ms-card ms-list ms-none">{renderText(top?.emptyText) || t("machine_status.no_running_sessions")}</div>;
  const byMem = main === "mem" && Number(top.rows[0].memoryBytes) > 0;
  return (
    <ul className="ms-card ms-list" ref={list}>
      {top.rows.map((row, i) => {
        const pct = Math.max(0, Math.min(100, Math.round((byMem ? row.memoryBarFraction : row.cpuBarFraction) * 100)));
        const primaryVal = renderText(byMem ? row.memoryValueText : row.cpuValueText);
        const sec = renderText(row.secondary);
        const body = (
          <>
            {usageIcon(row.agent)}
            <span className="ms-rb">
              <span className="ms-rt"><span>{renderText(row.title)}</span><span>{primaryVal}</span></span>
              <span className="ms-rm"><span className="ms-bar" style={{ width: `${pct}%` }} />{sec && <small>{sec}</small>}</span>
            </span>
          </>
        );
        return (
          <li key={row.id} className={`ms-u${i === 0 && !stale ? " top" : ""}`}>
            {top.selectedTab === MachineUsageTab.SESSIONS && row.sessionId ? <button type="button" className="ms-u" onClick={() => go("s/" + encodeURIComponent(row.sessionId))}>{body}</button> : body}
          </li>
        );
      })}
    </ul>
  );
}

function ModelUsagePreviewCard({ id, vm, go }: { id: string; vm: MachineStatusViewModel; go: Go }) {
  const mu = vm.modelUsage;
  if (!mu?.supported) return null;
  const unavail = renderText(mu.unsupportedFootnote);
  return (
    <>
      <div className="ms-sec">
        <h2>{t("machine_status.model_usage_title")}</h2>
        <button type="button" className="link" onClick={() => go(routeForUsage(id))}>
          {t("machine_status.model_usage_view_trends")}
        </button>
      </div>
      {mu.hasData ? (
        <ul className="ms-card ms-list ms-mu-list">
          {mu.rows.map((row) => (
            <li key={`${row.agent}:${row.model}`} className="ms-mu-row">
              <button
                type="button"
                className="ms-mu-btn"
                onClick={() => go(routeForUsage(id, row.agent, row.model))}
              >
                <AgentMark agent={row.agent} named />
                <span className="ms-rb">
                  <span className="ms-rt">
                    <span>{renderText(row.titleText)}</span>
                    <span>{renderText(row.totalTokensText)}</span>
                  </span>
                  <span className="ms-rm">
                    <small>{renderText(row.metaText)}</small>
                  </span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <div className="ms-card ms-list ms-none">{renderText(mu.emptyText) || t("machine_status.model_usage_empty")}</div>
      )}
      {unavail && <div className="ms-mu-foot">{unavail}</div>}
    </>
  );
}

function Skeleton({ name }: { name: string }) {
  return (
    <div className="body ms-body" role="region" aria-label={t("machine_status.title")}>
      <h1 className="ms-title">{name}</h1>
      <div className="ms-live"><span className="ms-dot idle" />{t("machine_status.loading")}</div>
      <div className="ms-cards">
        <section className="ms-card ms-hero"><div className="ms-top"><div><div className="ms-kick"><CpuIcon />{t("machine_status.cpu")}</div><div className="ms-xl">—</div></div></div><div className="ms-plot" /><div className="ms-axis"><span>{t("machine_status.axis_2min")}</span><span>{t("machine_status.axis_1min")}</span><span>{t("machine_status.axis_now")}</span></div></section>
        <div className="ms-grid"><div className="ms-card ms-tile"><span className="ms-kick"><MemoryIcon />{t("machine_status.memory")}</span><span className="ms-lg">—</span></div><div className="ms-card ms-tile"><span className="ms-kick"><DiskIcon />{t("machine_status.disk")}</span><span className="ms-lg">—</span></div></div>
      </div>
    </div>
  );
}

export function MachineStatusScreenView({ id, vm, visible = true, reduced = false, go, onBack, onRetry, onSelectTab, onScrub, onCleanup, overlaySlot }: {
  id: string; vm: MachineStatusViewModel; visible?: boolean; reduced?: boolean; go: Go; onBack: () => void;
  onRetry: () => void; onSelectTab: (tab: MachineUsageTab) => void; onScrub: (kind: MachineChartKind, sampleIndex: number, clear: boolean) => void;
  onCleanup: () => void; overlaySlot?: ReactNode;
}) {
  const [main, setMain] = useState<Main>("cpu");
  const cards = useRef<HTMLDivElement>(null);
  const stale = vm.pollState === MachinePollState.STALE;
  const swap = useCallback((next: Main) => {
    const beforeHero = !reduced ? cards.current?.querySelector<HTMLElement>(".ms-hero")?.getBoundingClientRect() : undefined;
    const beforeTile = !reduced ? cards.current?.querySelector<HTMLElement>(".ms-grid .ms-tile")?.getBoundingClientRect() : undefined;
    setMain(next);
    if (!beforeHero || !beforeTile) return;
    requestAnimationFrame(() => {
      const h = cards.current?.querySelector<HTMLElement>(".ms-hero"), tl = cards.current?.querySelector<HTMLElement>(".ms-grid .ms-tile");
      if (h) flipBox(h, beforeTile); if (tl) flipBox(tl, beforeHero);
    });
  }, [reduced]);

  const name = vm.machineName || id, disk = vm.disk, hasData = Boolean(vm.cpu?.hasValue || vm.memory?.hasValue || vm.disk?.hasValue);
  if (!vm.found && vm.pollState === MachinePollState.LOADING) {
    return <div className="page set ms-page"><NavBar onBack={onBack} /><div data-late="1"><Skeleton name={name} /></div></div>;
  }
  if (!vm.found || !vm.supported || (vm.pollState === MachinePollState.UNAVAILABLE && !hasData)) {
    return (
      <div className="page set ms-page">
        <NavBar onBack={onBack} />
        <div className="center ms-fail"><div><div className="et">{renderText(vm.unavailableTitle) || t("machine_status.unavailable")}</div>{vm.showRetry && <div className="big"><button className="link" onClick={onRetry}>{t("common.retry")}</button></div>}</div></div>
      </div>
    );
  }
  if (!hasData) return <div className="page set ms-page"><NavBar onBack={onBack} /><Skeleton name={name} /></div>;

  const moving = vm.pollState === MachinePollState.LIVE && visible, tab = vm.topUsage?.selectedTab ?? MachineUsageTab.SESSIONS;
  const agentVal = renderText(disk?.agentUsageValue), foot = [renderText(vm.uptimeText), renderText(vm.batteryText)].filter(Boolean).join(" · ");
  const activeChart = main === "cpu" ? vm.cpu?.chart : vm.memory?.chart, liveLabel = activeChart?.hasScrub ? renderText(activeChart.scrubAge) : renderText(vm.statusBadge);

  return (
    <div className={`page set ms-page${stale ? " is-stale" : ""}`}>
      <NavBar onBack={onBack} />
      <div className="body ms-body" role="region" aria-label={t("machine_status.title")}>
        <h1 className="ms-title">{name}</h1>
        <div className="ms-live">
          <span className={DOT_CLS[vm.statusDot] ?? "ms-dot idle"} /><span>{liveLabel}</span>
          {vm.showRetry && !activeChart?.hasScrub && <button type="button" className="link" onClick={onRetry}>{t("common.retry")}</button>}
        </div>
        <div className="ms-cards" ref={cards}>
          <Hero vm={vm} main={main} moving={moving} reduced={reduced} onScrub={(kind, idx) => onScrub(kind, idx, false)} onClearScrub={(kind) => onScrub(kind, 0, true)} />
          <div className="ms-grid">
            {main === "cpu" ? <MemTile vm={vm} onOpen={() => swap("mem")} /> : <CpuTile vm={vm} onOpen={() => swap("cpu")} />}
            <DiskTile vm={vm} onOpen={() => go("m/" + encodeURIComponent(id) + "/storage")} />
          </div>
          {disk?.showActions && (
            <div className="ms-card ms-rows">
              <button type="button" className="row act" onClick={onCleanup}>{renderText(disk.cleanupActionLabel) || t("machine_status.cleanup_action")}</button>
              <button type="button" className="row" onClick={() => go("m/" + encodeURIComponent(id) + "/storage")}>
                {renderText(disk.agentUsageLabel) || t("machine_status.agent_usage")}{agentVal && <span className="v">{agentVal}</span>}<ChevronRight className="chev" />
              </button>
            </div>
          )}
          <div className="ms-sec">
            <h2>{t("machine_status.top_usage")}</h2>
            <button type="button" className="link" onClick={() => onSelectTab(tab === MachineUsageTab.SESSIONS ? MachineUsageTab.PROCESSES : MachineUsageTab.SESSIONS)}>
              {tab === MachineUsageTab.SESSIONS ? t("machine_status.show_processes") : t("machine_status.show_sessions")}
            </button>
          </div>
          <Ranking vm={vm} stale={stale} main={main} go={go} />
          <ModelUsagePreviewCard id={id} vm={vm} go={go} />
          {foot && <div className="ms-foot">{foot}</div>}
        </div>
      </div>
      {overlaySlot}
    </div>
  );
}

export function MachineStatusPage({ remote, id, go, onBack }: { remote: Remote; id: string; go: Go; onBack: () => void }) {
  const host = useCoreHost(remote, go), visible = usePageVisible(), reduced = useReducedMotion();
  const vm = useMachineStatusView(host, id, visible ? MachinePollMode.LIVE : MachinePollMode.OFF);
  const [cleanupPrompt, setCleanupPrompt] = useState<string | null>(null);
  const stale = vm.pollState === MachinePollState.STALE;
  useEffect(() => {
    if (!stale || !visible) return;
    const timer = setInterval(() => host.tickClock(), 1000);
    return () => clearInterval(timer);
  }, [stale, visible, host]);
  useEffect(() => host.onCleanupPicker((eff) => { if (eff.machineId === id) setCleanupPrompt(t("machine_status.cleanup_prompt")); }), [host, id]);

  return (
    <MachineStatusScreenView id={id} vm={vm} visible={visible} reduced={reduced} go={go} onBack={onBack}
      onRetry={() => host.retryMachineStatus(id)} onSelectTab={(tab) => host.selectMachineStatusTab(id, tab)}
      onScrub={(kind, idx, clear) => host.scrubMachineStatus(id, kind, idx, clear)} onCleanup={() => host.cleanupMachineStatus(id)}
      overlaySlot={cleanupPrompt !== null && (
        <NewProjectSheet remote={remote} fixedMachine={id} onClose={() => setCleanupPrompt(null)} onDone={(machineId, path) => {
          addProject(remote, machineId, path); setCleanupPrompt(null);
          const draft = remote.drafts.get("new:" + path);
          remote.drafts.set("new:" + path, !draft ? cleanupPrompt : draft.includes(cleanupPrompt) ? draft : `${draft}\n\n${cleanupPrompt}`);
          remote.draftMachines.set(path, machineId); remote.focusProject = path; remote.touch(); go(routeForProject(path));
        }} />
      )}
    />
  );
}
