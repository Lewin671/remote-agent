import { localeStore, t, type Locale } from "./i18n";

export function formatCommaUint(n: number | bigint): string {
  const num = typeof n === "bigint" ? Number(n) : Math.max(0, Math.round(n));
  if (!Number.isFinite(num)) return "0";
  return Math.round(num).toLocaleString("en-US");
}

function formatScaled(val: number, unit: string): string {
  if (val < 100) {
    const rounded = Math.round(val * 10) / 10;
    const str = Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1);
    return str + unit;
  }
  return formatCommaUint(Math.round(val)) + unit;
}

export function formatTokens(n: number | bigint, locale: Locale = localeStore.snapshot()): string {
  const val = typeof n === "bigint" ? Number(n) : n;
  if (!Number.isFinite(val) || val <= 0) return "0";
  const english = locale === "en";
  if (english) {
    if (val < 1000) return formatCommaUint(val);
    if (val < 999_500) return formatScaled(val / 1000, "k");
    if (val < 999_500_000) return formatScaled(val / 1_000_000, "M");
    return formatScaled(val / 1_000_000_000, "B");
  }
  const wan = t("usage.unit_wan", undefined, locale);
  const yi = t("usage.unit_yi", undefined, locale);
  if (val < 10_000) return formatCommaUint(val);
  if (val < 99_950_000) return formatScaled(val / 10_000, wan);
  if (val < 100_000_000) return "1" + yi;
  return formatScaled(val / 100_000_000, yi);
}

export function formatCacheHit(rate: number): string {
  if (!Number.isFinite(rate) || rate <= 0) return "0%";
  if (rate >= 1) return "100%";
  const pct = rate * 100;
  if (pct < 1 || pct > 99) {
    const rounded = Math.round(pct * 10) / 10;
    const s = Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1);
    return `${s}%`;
  }
  return `${Math.round(pct)}%`;
}

export function formatSpeedValue(tokPerSec: number): string {
  if (!Number.isFinite(tokPerSec) || tokPerSec <= 0) return "0";
  if (tokPerSec < 10) {
    const rounded = Math.round(tokPerSec * 10) / 10;
    return Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1);
  }
  return formatCommaUint(Math.round(tokPerSec));
}

export function formatTtftValue(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return "0 ms";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 10_000) return `${(Math.round(ms / 100) / 10).toFixed(1)} s`;
  return `${Math.round(ms / 1000)} s`;
}

export function formatSignedPercent(pct: number, locale: Locale = localeStore.snapshot()): string {
  if (!Number.isFinite(pct) || Math.abs(pct) < 0.5) return t("usage.flat", undefined, locale);
  const r = Math.round(pct);
  return r > 0 ? `+${r}%` : `−${Math.abs(r)}%`;
}

export function formatSignedPoints(pts: number, locale: Locale = localeStore.snapshot()): string {
  if (!Number.isFinite(pts) || Math.abs(pts) < 0.5) return t("usage.flat", undefined, locale);
  const r = Math.round(pts);
  return r > 0 ? `+${r} pt` : `−${Math.abs(r)} pt`;
}

export function formatTtftDelta(curMs: number, prevMs: number, locale: Locale = localeStore.snapshot()): string {
  if (!Number.isFinite(curMs) || !Number.isFinite(prevMs) || prevMs <= 0) return "—";
  const diff = curMs - prevMs;
  if (Math.abs(diff) < 50) return t("usage.flat", undefined, locale);
  const sign = diff > 0 ? "+" : "−";
  const abs = Math.abs(diff);
  if (abs < 1000) return `${sign}${Math.round(abs)} ms`;
  if (abs < 10_000) return `${sign}${(Math.round(abs / 100) / 10).toFixed(1)} s`;
  return `${sign}${Math.round(abs / 1000)} s`;
}
