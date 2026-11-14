// What the computer-status screens draw, apart from the drawing: sizes in
// decimal units, the three rings, the colour a value takes, the ranking rows
// and the curve's geometry. The status itself (fetching, polling, the order
// of the ranking) is machine-status.ts; the words are machine_status.json.

import { localeStore, t, type Locale } from "./i18n";
import { ResourcePressure, type DiskStatus, type MachineSample, type MachineStatus, type ResourceUsage } from "./gen/remoteagent/v1/local_pb";

/** ink, orange (注意) or red (紧张): the colour a value takes; CPU never has one. */
export type Tone = "" | "warn" | "crit";

export const toneOf = (p: ResourcePressure | undefined): Tone =>
  p === ResourcePressure.CRITICAL ? "crit" : p === ResourcePressure.WARNING ? "warn" : "";

const number = (value: number, digits: [min: number, max: number], locale: Locale) =>
  new Intl.NumberFormat(locale, { minimumFractionDigits: digits[0], maximumFractionDigits: digits[1] }).format(value);

/** A size in GB as a bare number, decimal units: one decimal below 10, none
 * from 10 up ("1.2", "42"), and no ".0" ("16"). */
export function gbNumber(bytes: number | bigint, locale: Locale = localeStore.snapshot()): string {
  const gb = Number(bytes) / 1e9;
  return number(gb, [0, gb >= 9.95 ? 0 : 1], locale);
}

const GIB = 1024 ** 3;
const MIB = 1024 ** 2;

/** The memory in use, always with its decimal ("11.2"). Memory is counted in
 * binary units and written GB, as the system's activity monitor does (ruling 1). */
export const gbFixed = (bytes: number | bigint, locale: Locale = localeStore.snapshot()): string => number(Number(bytes) / GIB, [1, 1], locale);

/** Memory, swap and the figures of the ranking: binary units written GB and MB. */
export function memText(bytes: number | bigint, locale: Locale = localeStore.snapshot()): string {
  const value = Number(bytes);
  if (value >= GIB) {
    const gb = value / GIB;
    return t("machine_status.size_gb", { size: number(gb, [0, gb >= 9.95 ? 0 : 1], locale) }, locale);
  }
  return t("machine_status.size_mb", { size: number(Math.max(1, Math.round(value / MIB)), [0, 0], locale) }, locale);
}

/** A disk size with its unit, decimal as the Finder counts: "3.1 GB", "380 MB". */
export function sizeText(bytes: number | bigint, locale: Locale = localeStore.snapshot()): string {
  const value = Number(bytes);
  if (value >= 1e9) return t("machine_status.size_gb", { size: gbNumber(value, locale) }, locale);
  return t("machine_status.size_mb", { size: number(Math.max(1, Math.round(value / 1e6)), [0, 0], locale) }, locale);
}

/** A whole percent, as the rings and the CPU write it. */
export const percentText = (percent: number, locale: Locale = localeStore.snapshot()): string => `${number(Math.round(percent), [0, 0], locale)}%`;

export interface Ring {
  kind: "cpu" | "memory" | "disk";
  /** Drawn from 12 o'clock, clockwise; the disk's is what is used. */
  ratio: number;
  /** What is written under it ("23%", "42 GB"). */
  text: string;
  tone: Tone;
}

const clamp01 = (x: number) => (Number.isFinite(x) ? Math.min(1, Math.max(0, x)) : 0);

/** The three rings of a status: CPU, memory, and the disk's free space. */
export function ringsOf(s: MachineStatus, locale: Locale = localeStore.snapshot()): Ring[] {
  const memory = s.memoryTotalBytes ? Number(s.memoryUsedBytes) / Number(s.memoryTotalBytes) : 0;
  const disk = s.disk;
  const total = Number(disk?.totalBytes ?? 0n);
  return [
    { kind: "cpu", ratio: clamp01(s.cpuPercent / 100), text: percentText(s.cpuPercent, locale), tone: "" },
    { kind: "memory", ratio: clamp01(memory), text: percentText(memory * 100, locale), tone: toneOf(s.memoryPressure) },
    {
      kind: "disk",
      ratio: total ? clamp01(1 - Number(disk!.availableBytes) / total) : 0,
      text: disk ? t("machine_status.size_gb", { size: gbNumber(disk.availableBytes, locale) }, locale) : "—",
      tone: toneOf(disk?.pressure),
    },
  ];
}

/** The disk bar: what remote-agent uses, the rest that is used, in percent of the volume. */
export function diskBar(disk: DiskStatus, agentBytes: bigint | undefined): { agent: number; other: number } {
  const total = Number(disk.totalBytes);
  if (!total) return { agent: 0, other: 0 };
  const used = Math.max(0, total - Number(disk.availableBytes));
  const agent = Math.min(used, Number(agentBytes ?? 0n));
  return { agent: (agent / total) * 100, other: ((used - agent) / total) * 100 };
}

/** The time in seconds as the stale line writes it: seconds below a minute, minutes after. */
export function staleText(ageMs: number, locale: Locale = localeStore.snapshot()): string {
  const seconds = Math.max(0, Math.floor(ageMs / 1000));
  return seconds < 60
    ? t("machine_status.stale_seconds", { count: Math.max(1, seconds) }, locale)
    : t("machine_status.stale_minutes", { count: Math.floor(seconds / 60) }, locale);
}

/** How long the computer has been up: days and hours, hours and minutes, or minutes. */
export function uptimeText(seconds: number | bigint, locale: Locale = localeStore.snapshot()): string {
  const total = Number(seconds);
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const duration = days > 0
    ? t("machine_status.duration_days_hours", { days, hours }, locale)
    : hours > 0
      ? t("time.duration_hours_minutes", { hours, minutes }, locale)
      : t("machine_status.duration_minutes", { count: Math.max(1, minutes) }, locale);
  return t("machine_status.uptime", { duration }, locale);
}

export function batteryText(percent: number, charging: boolean, locale: Locale = localeStore.snapshot()): string {
  const value = Math.round(percent);
  return charging ? t("machine_status.battery_charging", { percent: value }, locale) : t("machine_status.battery", { percent: value }, locale);
}

/** The footer line: how long it has been up, and the battery where there is one. */
export function footerText(s: MachineStatus, locale: Locale = localeStore.snapshot()): string {
  const parts = [uptimeText(s.uptimeSeconds, locale)];
  if (s.battery) parts.push(batteryText(s.battery.percent, s.battery.charging, locale));
  return parts.join(" · ");
}

export const pressureWord = (p: ResourcePressure): string =>
  p === ResourcePressure.CRITICAL ? t("machine_status.pressure_critical")
    : p === ResourcePressure.WARNING ? t("machine_status.pressure_high")
      : t("machine_status.pressure_normal");

export const diskWord = (p: ResourcePressure): string =>
  p === ResourcePressure.CRITICAL ? t("machine_status.disk_critical")
    : p === ResourcePressure.WARNING ? t("machine_status.disk_low")
      : t("machine_status.disk_ok");

/** A state after which the disk's number, its unit and its bar take its colour. */
export const diskTight = (p: ResourcePressure | undefined): boolean => p === ResourcePressure.WARNING || p === ResourcePressure.CRITICAL;

/** Where the pressure dot sits, 0..1, on the green-to-red scale; none when the system gives none. */
export function pressurePosition(s: MachineStatus): number | undefined {
  if (s.memoryPressure === ResourcePressure.UNSPECIFIED) return undefined;
  return s.memoryPressurePosition === undefined ? undefined : clamp01(s.memoryPressurePosition);
}

// --- the ranking ---

export type RankKind = "sessions" | "processes";
export const RANK_ROWS = 8;
export const rankMetric = (r: ResourceUsage, memory: boolean): number => (memory ? Number(r.memoryBytes) : r.cpuPercent);

/** Each row's bar: the row's main figure against the first row's, 100 at the top. */
export function barPercent(row: ResourceUsage, first: ResourceUsage | undefined, memory: boolean): number {
  const top = first ? rankMetric(first, memory) : 0;
  return top > 0 ? Math.min(100, (rankMetric(row, memory) / top) * 100) : 0;
}

// --- the curve ---

/** A 116-second window stays inside 60 samples while the right edge trails by up to 2 seconds. */
export const WINDOW_MS = 116_000;
/** Samples further apart than this had nobody looking between them: the curve breaks there. */
export const GAP_MS = 10_000;
/** The "now" dot sits this far inside the right edge, so that it is not cut. */
export const DOT_INSET = 4;
/** The line and the dot keep this far from the top and the bottom. */
const PAD = 4;

export interface Point { t: number; v: number }

/** The samples to draw: real points only, in time order, from the point the curve restarts at. */
export function curvePoints(s: MachineStatus, memory: boolean, since: bigint): Point[] {
  const from = Number(since);
  const list: MachineSample[] = s.samples.length ? [...s.samples] : [];
  const points = list
    .filter((x) => Number(x.timeMs) >= from)
    .map((x) => ({ t: Number(x.timeMs), v: memory ? Number(x.memoryUsedBytes) : x.cpuPercent }))
    .sort((a, b) => a.t - b.t);
  if (points.length) return points;
  // A first answer without history: the one point it is.
  return [{ t: Number(s.observedMs), v: memory ? Number(s.memoryUsedBytes) : s.cpuPercent }];
}

/** A continuous chart clock. On-time samples trail by at most 2 s. A poll gap
 * catches up to the newest point in 500 ms, cubic ease-out, without a jump.
 * Retargeting keeps the original deadline; old snapshots never rewind it. */
export class ChartTimeline {
  private end?: number;
  private latest = 0;
  private lastNow = 0;
  private catchup?: { from: number; target: number; start: number; deadline: number };

  advance(nowMs: number, firstMs: number, latestMs: number, moving = true): number {
    if (latestMs <= 0) return 0;
    if (this.end === undefined || !moving) {
      this.end = moving ? Math.max(firstMs, latestMs - 2000) : latestMs;
      this.latest = latestMs;
      this.lastNow = nowMs;
      this.catchup = undefined;
      return this.end;
    }
    const now = Math.max(this.lastNow, nowMs);
    const c = this.catchup;
    if (c) {
      const p = Math.min(1, (now - c.start) / (c.deadline - c.start));
      this.end = c.from + (c.target - c.from) * (1 - (1 - p) ** 3);
      if (p === 1) this.catchup = undefined;
    } else {
      this.end = Math.min(this.latest, this.end + now - this.lastNow);
    }
    this.lastNow = now;
    if (latestMs > this.latest) {
      this.latest = latestMs;
      if (this.catchup || latestMs - this.end > 2000) {
        this.catchup = { from: this.end, target: latestMs, start: now, deadline: this.catchup?.deadline ?? now + 500 };
      }
    }
    return this.end;
  }
}

/** The points as they stand at display time `at`: those up to it, then the
 * head, interpolated where `at` falls between two. */
export function visiblePoints(points: readonly Point[], at: number): Point[] {
  const out: Point[] = [];
  for (let i = 0; i < points.length; i++) {
    const p = points[i];
    if (p.t <= at) out.push(p);
    else {
      const prev = points[i - 1];
      if (prev && p.t - prev.t <= GAP_MS && at > prev.t) out.push({ t: at, v: prev.v + ((p.v - prev.v) * (at - prev.t)) / (p.t - prev.t) });
      break;
    }
  }
  return out.filter((p) => p.t >= at - WINDOW_MS - GAP_MS);
}

export const xOf = (t: number, at: number, width: number): number => (width - DOT_INSET) * (1 - (at - t) / WINDOW_MS);
export const yOf = (v: number, max: number, height: number): number => PAD + (1 - Math.min(1, Math.max(0, max > 0 ? v / max : 0))) * (height - 2 * PAD);

interface Pt { x: number; y: number }

/** A smooth path through the points that never overshoots them (monotone cubic). */
function smooth(pts: readonly Pt[]): string {
  const n = pts.length;
  if (n === 1) return `M${pts[0].x.toFixed(2)},${pts[0].y.toFixed(2)}`;
  const dx: number[] = [];
  const slope: number[] = [];
  for (let i = 0; i < n - 1; i++) {
    dx.push(pts[i + 1].x - pts[i].x);
    slope.push(dx[i] === 0 ? 0 : (pts[i + 1].y - pts[i].y) / dx[i]);
  }
  const tangent = [slope[0]];
  for (let i = 1; i < n - 1; i++) tangent.push(slope[i - 1] * slope[i] <= 0 ? 0 : (slope[i - 1] + slope[i]) / 2);
  tangent.push(slope[n - 2]);
  for (let i = 0; i < n - 1; i++) {
    if (slope[i] === 0) { tangent[i] = 0; tangent[i + 1] = 0; continue; }
    const a = tangent[i] / slope[i];
    const b = tangent[i + 1] / slope[i];
    const h = Math.hypot(a, b);
    if (h > 3) { tangent[i] = (3 * a * slope[i]) / h; tangent[i + 1] = (3 * b * slope[i]) / h; }
  }
  let d = `M${pts[0].x.toFixed(2)},${pts[0].y.toFixed(2)}`;
  for (let i = 0; i < n - 1; i++) {
    const w = dx[i] / 3;
    d += ` C${(pts[i].x + w).toFixed(2)},${(pts[i].y + w * tangent[i]).toFixed(2)} ${(pts[i + 1].x - w).toFixed(2)},${(pts[i + 1].y - w * tangent[i + 1]).toFixed(2)} ${pts[i + 1].x.toFixed(2)},${pts[i + 1].y.toFixed(2)}`;
  }
  return d;
}

/** The curve's line and its area for the points at display time `at`.
 * Runs of points a gap apart are drawn apart: no line, no fill between. */
export function curvePaths(points: readonly Point[], at: number, max: number, width: number, height: number): { line: string; area: string; head?: Pt } {
  const shown = visiblePoints(points, at);
  const runs: Pt[][] = [];
  let prev: Point | undefined;
  for (const p of shown) {
    const pt = { x: xOf(p.t, at, width), y: yOf(p.v, max, height) };
    if (!prev || p.t - prev.t > GAP_MS) runs.push([pt]);
    else runs.at(-1)!.push(pt);
    prev = p;
  }
  let line = "";
  let area = "";
  for (const run of runs) {
    // A point alone leaves no line: it is only the dot, if it is the newest.
    if (run.length < 2) continue;
    const d = smooth(run);
    line += (line ? " " : "") + d;
    area += (area ? " " : "") + `${d} L${run.at(-1)!.x.toFixed(2)},${height} L${run[0].x.toFixed(2)},${height} Z`;
  }
  const last = shown.at(-1);
  return { line, area, head: last ? { x: xOf(last.t, at, width), y: yOf(last.v, max, height) } : undefined };
}

/** The sample nearest x, if one lies within a sampling interval of it; none over a gap. */
export function sampleAt(points: readonly Point[], x: number, at: number, width: number): Point | undefined {
  if (!points.length) return undefined;
  const t = at - (1 - x / (width - DOT_INSET)) * WINDOW_MS;
  let best: Point | undefined;
  for (const p of points) if (p.t <= at + 1 && (!best || Math.abs(p.t - t) < Math.abs(best.t - t))) best = p;
  return best && Math.abs(best.t - t) <= GAP_MS / 2 ? best : undefined;
}

/** How long before the newest sample this one is, in whole seconds rounded to 2. */
export const secondsBefore = (point: Point, newest: Point): number => Math.round((newest.t - point.t) / 2000) * 2;
