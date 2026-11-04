// Account quota windows as the device pages show them (architecture 账户
// 额度): used share, reset time, and whether the reading can still be
// trusted. A missing value is never shown as 0%, and a reset time that
// passed does not mean the quota is back.

import type { Agent, RateLimit } from "./gen/remoteagent/v1/event_pb";
import { QuotaState, type AgentQuota, type MachineMeta, type QuotaWindow } from "./gen/remoteagent/v1/remote_pb";
import { agentInfo, normalizePlanName } from "./agents";

export interface QuotaRow {
  label: string; // 5 小时, 7 天
  windowName?: string; // 5 小时, 7 天 (duration / short name for summary line)
  used: string; // 已用 62%, 已用尽, 未获取
  percent?: number;
  reset: string; // 4 小时 29 分后重置 · 今天 14:10
  tone: "" | "warn" | "full" | "stale";
  note: string; // why the reading may be old
}

const days = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];

/** How long until a reset, rounded down: to the hour from a day on, to
 * the minute below that. */
function untilReset(ms: number): string {
  const m = Math.floor(ms / 60_000);
  if (m < 1) return "即将重置";
  if (m < 60) return `${m} 分钟后重置`;
  if (m < 24 * 60) return `${Math.floor(m / 60)} 小时 ${m % 60} 分后重置`;
  return `${Math.floor(m / (24 * 60))} 天 ${Math.floor((m % (24 * 60)) / 60)} 小时后重置`;
}

// Shanghai is UTC+8 without DST: read the result with the getUTC* methods.
const beijing = (t: number) => new Date(t + 8 * 60 * 60 * 1000);
const dayOf = (d: Date) => d.toISOString().slice(0, 10);
const clock = (d: Date) => `${d.getUTCHours()}:${String(d.getUTCMinutes()).padStart(2, "0")}`;

/** How long until the reset, then its day and time. The instant is shown
 * in Beijing regardless of the device timezone; the page says so once. */
function resetTime(ms: number, now: number): string {
  const d = beijing(ms);
  const today = beijing(now);
  const day = dayOf(d) === dayOf(today) ? "今天" : `${d.getUTCMonth() + 1}月${d.getUTCDate()}日 ${days[d.getUTCDay()]}`;
  return `${untilReset(ms - now)} · ${day} ${clock(d)}`;
}

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
  const pct = l.usedPercent;
  const resetMs = Number(l.resetsAt) * 1000;
  const passed = resetMs > 0 && resetMs <= now;
  let reset = "";
  if (resetMs > 0 && !passed) {
    reset = resetTime(resetMs, now);
  }
  const used = pct >= 100 ? "已用尽" : `已用 ${Math.floor(pct)}%`;
  if (passed) return { label, used: "等待更新", percent: pct, reset: "", tone: "stale", note: `上次记录 ${used}；重置时间已过，不能据此确认额度已经恢复` };
  if (!online) return { label, used, percent: pct, reset, tone: "stale", note: "上次记录：电脑离线，可能已变化" };
  return { label, used, percent: pct, reset, tone: pct >= 100 ? "full" : pct >= 80 ? "warn" : "", note: "" };
}

// --- MachineMeta.quotas (daemons since ed1a14d) ---

export interface QuotaGroup {
  title: string;
  rows: (QuotaRow & { key: string })[];
}

export interface QuotaView {
  agent: Agent;
  title: string; // Codex · Pro Lite, Claude Code · 上次记录, … · 会话 fc84
  kind: "ok" | "waiting" | "failed" | "unsupported";
  head: string; // for waiting and unsupported: 暂未获取额度 / 暂不支持读取额度
  detail: string; // the daemon's why, for unsupported
  error: string; // for failed
  rows: (QuotaRow & { key: string })[];
  groups: QuotaGroup[];
  banner: string; // 5 小时额度即将用尽, 额度已用尽
  seen: string; // 最近收到 10:51, once for all the rows
  stale: boolean;
}

/** A plan as people call it: Pro Lite for prolite, Max for claude_max. */
export function planName(plan: string, agent?: Agent): string {
  return agent ? agentInfo(agent).planName(plan) : normalizePlanName(plan);
}

/** When a reading arrived, in Beijing like the reset times beside it. */
function when(ms: number, now: number): string {
  const d = beijing(ms);
  if (dayOf(d) === dayOf(beijing(now))) return clock(d);
  if (dayOf(d) === dayOf(beijing(now - 86_400_000))) return `昨天 ${clock(d)}`;
  return `${d.getUTCMonth() + 1}月${d.getUTCDate()}日 ${clock(d)}`;
}

function windowRow(
  w: QuotaWindow,
  label: string,
  now: number,
  stale: boolean,
  useWindowMinutes = true,
): QuotaRow & { key: string } {
  const key = w.name;
  const windowName = w.windowMinutes > 0n ? windowLabel(Number(w.windowMinutes)) : label;
  if (useWindowMinutes && w.windowMinutes > 0n) label = windowName;
  if (w.usedPercent === undefined) return { key, label, windowName, used: "未获取", reset: "", tone: "", note: "没有收到这项额度数据" };
  const pct = w.usedPercent;
  const used = pct >= 100 ? "已用尽" : `已用 ${Math.floor(pct)}%`;
  const resetMs = Number(w.resetsAtMs);
  if (resetMs > 0 && resetMs <= now)
    return { key, label, windowName, used: "等待更新", percent: pct, reset: "", tone: "stale", note: `上次${used}；重置时间已过，不能据此确认额度已经恢复` };
  let reset = "";
  if (resetMs > 0) reset = resetTime(resetMs, now);
  return {
    key,
    label,
    windowName,
    used: stale ? `上次${used}` : used,
    percent: pct,
    reset,
    tone: stale ? "stale" : pct >= 100 ? "full" : pct >= 80 ? "warn" : "",
    note: "",
  };
}

/** Each CLI's account quota on a machine, as the device pages show it:
 * one view per reported entry (several for one CLI when accounts or
 * sessions cannot be told apart; never added together), and
 * 暂不支持读取额度 for an installed CLI the daemon reports nothing for. */
export function quotaViews(quotas: AgentQuota[], agents: Agent[], now: number, online: boolean): QuotaView[] {
  const out: QuotaView[] = [];
  const norm = (a: Agent) => agentInfo(a).agent;
  for (const agent of agents.map(norm)) {
    const info = agentInfo(agent);
    const name = info.name;
    const mine = quotas.filter((q) => norm(q.agent) === agent);
    const base = { agent, head: "", detail: "", error: "", rows: [], groups: [], banner: "", seen: "", stale: false };
    if (!mine.length) {
      out.push({ ...base, title: name, kind: "unsupported", head: "暂不支持读取额度" });
      continue;
    }
    for (const q of mine) {
      const who = mine.length > 1 ? (q.account ? ` · 账户 ${q.account}` : q.sourceSessionId ? ` · 会话 ${q.sourceSessionId.slice(-4)}` : "") : "";
      if (q.state === QuotaState.WAITING || (q.state === QuotaState.UNSPECIFIED && !q.windows.length)) {
        out.push({ ...base, title: name + who, kind: "waiting", head: "暂未获取额度", detail: `等待 ${name} 返回额度数据` });
        continue;
      }
      if (q.state === QuotaState.UNSUPPORTED) {
        out.push({ ...base, title: name + who, kind: "unsupported", head: "暂不支持读取额度", detail: q.detail });
        continue;
      }
      const failed = q.state === QuotaState.FAILED;
      const stale = failed || !online;
      const observed = q.observedMs > 0n ? when(Number(q.observedMs), now) : "";
      const heard = failed ? "最近成功收到" : "最近收到";
      const byName = new Map(q.windows.map((w) => [w.name, w]));
      const known = info.quotaWindows;
      // Only the windows the account has: a plan with one window is not
      // shown a second one that never arrives.
      const ordered: QuotaWindow[] = [
        ...known.filter((n) => byName.has(n)).map((n) => byName.get(n)!),
        ...q.windows.filter((w) => !known.includes(w.name)),
      ];
      const rows: (QuotaRow & { key: string })[] = [];
      const groupMap = new Map<string, (QuotaRow & { key: string })[]>();
      for (const w of ordered) {
        const desc = info.quotaWindow(w.name);
        const useMinutes = desc.group === null && known.includes(w.name);
        const row = windowRow(w, desc.label, now, stale, useMinutes);
        rows.push(row);
        if (desc.group !== null) {
          let list = groupMap.get(desc.group);
          if (!list) groupMap.set(desc.group, (list = []));
          list.push(row);
        }
      }
      if (failed && !q.windows.length) {
        out.push({ ...base, title: name + who, kind: "failed", error: `没能读取 ${name} 额度${q.detail ? "：" + q.detail : ""}`, head: "暂未获取额度" });
        continue;
      }
      const got = rows.filter((r) => r.percent !== undefined && r.tone !== "stale");
      const full = got.find((r) => r.tone === "full");
      const warn = got.find((r) => r.tone === "warn");
      const prefix = name + (q.plan ? ` · ${agentInfo(q.agent).planName(q.plan)}` : "") + who;
      const staleSuffix = stale ? " · 上次记录" : "";
      const title = prefix + staleSuffix;
      const groups: QuotaGroup[] =
        groupMap.size > 0
          ? [...groupMap.entries()].map(([g, grows], idx) => ({
              title: idx === 0 ? `${prefix} · ${g}${staleSuffix}` : g,
              rows: grows,
            }))
          : rows.length > 0
            ? [{ title, rows }]
            : [];
      out.push({
        ...base,
        title,
        kind: failed ? "failed" : "ok",
        head: "暂未获取额度",
        seen: observed ? `${heard} ${observed}` : "",
        error: failed ? `没能读取 ${name} 额度${q.detail ? "：" + q.detail : ""}` : "",
        rows,
        groups,
        stale,
        banner: full ? `${full.label}额度已用尽` : warn ? `${warn.label}额度即将用尽` : "",
      });
    }
  }
  return out;
}

/** One line for a CLI on the machine page: its most-used window, or its state. */
export function quotaSummary(views: QuotaView[]): string {
  return views
    .map((v) => {
      if (v.kind === "waiting" || v.kind === "unsupported" || !v.rows.length) return v.head || "暂未获取额度";
      const got = v.rows.filter((r) => r.used !== "未获取" && r.percent !== undefined);
      if (!got.length) return "暂未获取额度" + (v.stale ? "（上次记录）" : "");
      const tightest = got.reduce((a, b) => ((b.percent ?? -1) > (a.percent ?? -1) ? b : a));
      const win = tightest.windowName || tightest.label;
      const staleSuffix = v.stale && !tightest.used.includes("上次") ? "（上次记录）" : "";
      return `${tightest.used} · ${win}${staleSuffix}`;
    })
    .join("；");
}

/** A machine's quota views, from the quotas in its metadata. */
export function machineQuotaViews(meta: MachineMeta | undefined, agents: Agent[], now: number, online: boolean): QuotaView[] {
  return quotaViews(meta?.quotas ?? [], agents, now, online);
}
