// Account quota windows as the device pages show them (architecture 账户
// 额度): used share, reset time, and whether the reading can still be
// trusted. A missing value is never shown as 0%, and a reset time that
// passed does not mean the quota is back.

import type { RateLimit } from "./gen/remoteagent/v1/event_pb";

export interface QuotaRow {
  label: string; // 5 小时, 7 天
  used: string; // 已用 62%, 已用尽, 未获取
  percent?: number;
  reset: string; // 14:10 重置, 周四 10:00 重置
  tone: "" | "warn" | "full" | "stale";
  note: string; // why the reading may be old
}

const days = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];

export function windowLabel(minutes: number): string {
  if (minutes >= 24 * 60 && minutes % (24 * 60) === 0) return `${minutes / (24 * 60)} 天`;
  if (minutes >= 60) return `${Math.round(minutes / 60)} 小时`;
  return `${minutes} 分钟`;
}

/** One window of a CLI account's quota. online: the machine that reported
 * it is connected now. */
export function quotaRow(l: RateLimit | undefined, fallbackLabel: string, now: number, online: boolean): QuotaRow {
  if (!l) return { label: fallbackLabel, used: "未获取", reset: "", tone: "", note: "没有收到这项额度数据" };
  const label = l.windowMinutes > 0n ? windowLabel(Number(l.windowMinutes)) : fallbackLabel;
  const pct = Math.round(l.usedPercent);
  const resetMs = Number(l.resetsAt) * 1000;
  const passed = resetMs > 0 && resetMs <= now;
  let reset = "";
  if (resetMs > 0 && !passed) {
    const d = new Date(resetMs);
    const hm = `${d.getHours()}:${String(d.getMinutes()).padStart(2, "0")}`;
    reset = `${d.toDateString() === new Date(now).toDateString() ? "" : days[d.getDay()] + " "}${hm} 重置`;
  }
  const used = pct >= 100 ? "已用尽" : `已用 ${pct}%`;
  if (passed) return { label, used: "等待更新", percent: pct, reset: "", tone: "stale", note: `上次记录 ${used}；重置时间已过，不能据此确认额度已经恢复` };
  if (!online) return { label, used, percent: pct, reset, tone: "stale", note: "上次记录：电脑离线，可能已变化" };
  return { label, used, percent: pct, reset, tone: pct >= 100 ? "full" : pct >= 80 ? "warn" : "", note: "" };
}
