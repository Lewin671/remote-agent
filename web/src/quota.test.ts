import { t as message } from "./i18n";
import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import { RateLimitSchema } from "./gen/remoteagent/v1/event_pb";
import { planName, quotaRow, quotaSummary, quotaViews } from "./quota";
import { Agent } from "./gen/remoteagent/v1/event_pb";
import { AgentQuotaSchema, QuotaState, QuotaWindowSchema } from "./gen/remoteagent/v1/remote_pb";
import { describe } from "vitest";

const now = Date.parse("2026-09-30T09:40:00+08:00");
const lim = (usedPercent: number, windowMinutes: number, resetInMin: number) => create(RateLimitSchema, { usedPercent, windowMinutes: BigInt(windowMinutes), resetsAt: BigInt(Math.floor((now + resetInMin * 60_000) / 1000)) });

it("shows use and reset, warns from 80% and calls 100% used up", () => {
  expect(quotaRow(lim(62, 300, 270), message("quota.hr", { count: 5 }), now, true)).toMatchObject({ label: message("quota.hr", { count: 5 }), left: "剩 38%", reset: "4 小时 30 分后重置 · 今天 14:10", tone: "" });
  expect(quotaRow(lim(92, 300, 30), message("quota.hr", { count: 5 }), now, true).tone).toBe("warn");
  expect(quotaRow(lim(100, 10080, 3 * 1440), message("quota.days", { count: 7 }), now, true)).toMatchObject({ label: message("quota.days", { count: 7 }), left: message("quota.exhausted_quota"), tone: "full" });
});

it("never shows a missing window as 0%", () => {
  const r = quotaRow(undefined, "7 天", now, true);
  expect(r.left).toBe(message("quota.unavailable"));
  expect(r.percent).toBeUndefined();
});

it("keeps an old reading as such: offline, or past its reset", () => {
  expect(quotaRow(lim(62, 300, 60), message("quota.hr", { count: 5 }), now, false)).toMatchObject({ tone: "stale", left: "剩 38%" });
  expect(quotaRow(lim(62, 300, -5), message("quota.hr", { count: 5 }), now, true)).toMatchObject({ tone: "stale", left: message("quota.awaiting_update") });
});

describe("account quota from MachineMeta.quotas (Go ed1a14d)", () => {
  const at = (h: number, m = 0) => Date.parse("2026-09-30T00:00:00+08:00") + (h * 60 + m) * 60_000;
  const q = (init: object) => create(AgentQuotaSchema, { observedMs: BigInt(now), ...init } as never);
  const w = (name: string, used: number | undefined, resetH: number | undefined, minutes: number) =>
    create(QuotaWindowSchema, { name, usedPercent: used, resetsAtMs: resetH === undefined ? 0n : BigInt(at(resetH, 10)), windowMinutes: BigInt(minutes) });
  const now = at(9, 41);

  it("shows each window's use, reset and when it was last heard, per CLI", () => {
    const [v] = quotaViews([q({ agent: Agent.CODEX, state: QuotaState.OK, plan: "Pro Lite", observedMs: BigInt(at(9, 40)), windows: [w("primary", 62, 14, 300), w("secondary", 34, 20, 10080)] })], [Agent.CODEX], now, true);
    expect(v.title).toBe("Codex · Pro Lite");
    expect(v.rows.map((r) => [r.label, r.left, r.reset, r.tone])).toEqual([
      [message("quota.hr", { count: 5 }), "剩 38%", "4 小时 29 分后重置 · 今天 14:10", ""],
      [message("quota.days", { count: 7 }), "剩 66%", "10 小时 29 分后重置 · 今天 20:10", ""],
    ]);
    expect(v.seen).toBe("最近收到 9:40");
  });

  it("marks a missing usage 未获取, never 0%, and a missing reset time by leaving it out", () => {
    const [v] = quotaViews([q({ agent: Agent.CLAUDE, state: QuotaState.OK, observedMs: BigInt(at(9, 40)), windows: [w("five_hour", 92, 13, 300), w("seven_day", undefined, undefined, 10080)] })], [Agent.CLAUDE], now, true);
    expect(v.rows[0]).toMatchObject({ left: "剩 8%", tone: "warn" });
    expect(v.rows[1]).toMatchObject({ label: message("quota.days", { count: 7 }), left: message("quota.unavailable"), reset: "" });
    expect(v.rows[1].percent).toBeUndefined();
    expect(v.banner).toBe("Claude Code · 5 小时额度即将用尽，不会自动切换 CLI 或模型");
    // A window the account does not have is left out: a plan with only a
    // weekly window is not shown a second 7 天 that never arrives.
    const [only] = quotaViews([q({ agent: Agent.CODEX, state: QuotaState.OK, plan: "prolite", observedMs: BigInt(at(9, 40)), windows: [w("primary", 72, 13, 10080)] })], [Agent.CODEX], now, true);
    expect(only.title).toBe("Codex · Pro Lite");
    expect(only.rows.map((r) => [r.label, r.left])).toEqual([[message("quota.days", { count: 7 }), "剩 28%"]]);
    expect(planName("claude_max")).toBe("Max");
    expect(planName("Team Plus")).toBe("Team Plus");
  });

  it("says waiting, unsupported, and keeps a failed read's last record with its time", () => {
    const views = quotaViews(
      [
        q({ agent: Agent.CLAUDE, state: QuotaState.WAITING }),
        q({ agent: Agent.CODEX, state: QuotaState.FAILED, detail: "rollout unreadable", observedMs: BigInt(at(9, 20)), windows: [w("primary", 28, 13, 300)] }),
      ],
      [Agent.CODEX, Agent.CLAUDE],
      now,
      true,
    );
    const codex = views.find((v) => v.agent === Agent.CODEX)!;
    const claude = views.find((v) => v.agent === Agent.CLAUDE)!;
    expect(claude).toMatchObject({ kind: "waiting", head: message("quota.quota_not_available_yet") });
    expect(codex).toMatchObject({ kind: "failed", error: "没能读取 Codex 额度：rollout unreadable", title: "Codex · 上次记录" });
    expect(codex.rows[0]).toMatchObject({ left: "上次剩 72%", tone: "stale", reset: "3 小时 29 分后重置 · 今天 13:10" });
    expect(codex.seen).toBe("最近成功收到 9:20");
    const [unsup] = quotaViews([q({ agent: Agent.CLAUDE, state: QuotaState.UNSUPPORTED, detail: "托管设置固定了状态栏" })], [Agent.CLAUDE], now, true);
    expect(unsup).toMatchObject({ kind: "unsupported", head: message("quota.quota_lookup_is_not_supported_yet"), detail: "托管设置固定了状态栏" });
    // A CLI the daemon has no entry for is not supported by it.
    expect(quotaViews([q({ agent: Agent.CODEX, state: QuotaState.WAITING })], [Agent.CODEX, Agent.CLAUDE], now, true).find((v) => v.agent === Agent.CLAUDE)?.kind).toBe("unsupported");
  });

  it("greys an offline machine's reading and waits for an update after a reset time passed", () => {
    const [off] = quotaViews([q({ agent: Agent.CLAUDE, state: QuotaState.OK, observedMs: BigInt(at(9, 40) - 86_400_000), windows: [w("five_hour", 92, 8, 300)] })], [Agent.CLAUDE], now, false);
    expect(off.title).toBe("Claude Code · 上次记录");
    expect(off.rows[0]).toMatchObject({ left: message("quota.awaiting_update"), tone: "stale", note: "上次剩 8%；重置时间已过，不能据此确认额度已经恢复" });
  });

  it("keeps accounts and sessions that cannot be told apart separate, and never adds CLIs together", () => {
    const views = quotaViews(
      [
        q({ agent: Agent.CLAUDE, state: QuotaState.OK, account: "a1b2", observedMs: BigInt(at(9, 40)), windows: [w("five_hour", 10, 13, 300)] }),
        q({ agent: Agent.CLAUDE, state: QuotaState.OK, sourceSessionId: "f084387df054fc84", observedMs: BigInt(at(9, 30)), windows: [w("five_hour", 50, 13, 300)] }),
        q({ agent: Agent.CODEX, state: QuotaState.OK, observedMs: BigInt(at(9, 40)), windows: [w("primary", 20, 13, 300)] }),
      ],
      [Agent.CODEX, Agent.CLAUDE],
      now,
      true,
    );
    expect(views.map((v) => v.title)).toEqual(["Codex", "Claude Code · 账户 a1b2", "Claude Code · 会话 fc84"]);
    expect(quotaSummary(views.filter((v) => v.agent === Agent.CODEX))).toBe("剩 80% · 5 小时");
  });

  it("groups Antigravity quota windows by model family and keeps unknown buckets in their own group", () => {
    const [v] = quotaViews(
      [
        q({
          agent: Agent.ANTIGRAVITY,
          state: QuotaState.OK,
          plan: "Google AI Pro",
          observedMs: BigInt(at(9, 41)),
          windows: [
            w("gemini-5h", 18, 14, 300),
            w("gemini-weekly", 12, 20, 10080),
            w("gemini-argon-wtu-weekly", 35, 19, 10080),
            w("3p-5h", 40, 13, 300),
            w("3p-weekly", 26, 18, 10080),
            w("custom-bucket", 5, 15, 60),
          ],
        }),
      ],
      [Agent.ANTIGRAVITY],
      now,
      true,
    );
    expect(v.title).toBe("Antigravity · Google AI Pro");
    expect(v.groups.map((g) => [g.title, g.rows.map((r) => [r.label, r.left])])).toEqual([
      [
        "Antigravity · Google AI Pro · Gemini",
        [
          [message("quota.named_hours", { name: "Gemini", count: 5 }), "剩 82%"],
          [message("quota.named_days", { name: "Gemini", count: 7 }), "剩 88%"],
          [message("quota.named_days", { name: "Gemini 4 Argon", count: 7 }), "剩 65%"],
        ],
      ],
      [
        message("quota.third_party_models_claude_gpt_oss"),
        [
          [message("quota.named_hours", { name: message("quota.third_party_models"), count: 5 }), "剩 60%"],
          [message("quota.named_days", { name: message("quota.third_party_models"), count: 7 }), "剩 74%"],
        ],
      ],
      ["custom-bucket", [["custom-bucket", "剩 95%"]]],
    ]);
    expect(v.seen).toBe("最近收到 9:41");
    expect(quotaSummary([v])).toBe("剩 60% · 第三方模型");
  });
});

it("uses reported durations and raw percentages for warning thresholds", () => {
  const window = create(QuotaWindowSchema, { name: "primary", usedPercent: 99.6, windowMinutes: 120n });
  const q = create(AgentQuotaSchema, { agent: Agent.CODEX, state: QuotaState.OK, observedMs: BigInt(now), windows: [window] });
  const [v] = quotaViews([q], [Agent.CODEX], now, true);
  expect(v.rows[0]).toMatchObject({ label: "2 小时", left: "剩 1%", percent: 99.6, tone: "warn" });
  expect(v.banner).toBe("Codex · 2 小时额度即将用尽，不会自动切换 CLI 或模型");
  expect(quotaRow(lim(99.6, 120, 60), message("quota.hr", { count: 5 }), now, true)).toMatchObject({ left: "剩 1%", tone: "warn" });
  expect(quotaRow(lim(79.6, 120, 60), message("quota.hr", { count: 5 }), now, true).tone).toBe("");
  window.usedPercent = 79.6;
  expect(quotaViews([q], [Agent.CODEX], now, true)[0].banner).toBe("");
});


it("uses Beijing dates across midnight regardless of the client timezone", () => {
  const now = Date.parse("2026-10-02T15:30:00Z");
  const reset = Date.parse("2026-10-02T16:10:00Z");
  const l = create(RateLimitSchema, { usedPercent: 95, resetsAt: BigInt(reset / 1000) });
  expect(quotaRow(l, message("quota.days", { count: 7 }), now, true).reset).toBe("40 分钟后重置 · 10月3日 周六 0:10");
  const q = create(AgentQuotaSchema, { agent: Agent.CODEX, state: QuotaState.OK, windows: [create(QuotaWindowSchema, { name: "secondary", usedPercent: 95, resetsAtMs: BigInt(reset) })] });
  expect(quotaViews([q], [Agent.CODEX], now, true)[0].rows[0].reset).toBe("40 分钟后重置 · 10月3日 周六 0:10");
});

it("says how long until a reset: to the hour from a day on, to the minute below", () => {
  const reset = (min: number) => quotaRow(lim(10, 10080, min), "7 天", now, true).reset;
  expect(reset(6 * 1440 + 4 * 60 + 29)).toBe("6 天 4 小时后重置 · 10月6日 周二 14:09");
  expect(reset(1440)).toBe("1 天 0 小时后重置 · 10月1日 周四 9:40");
  expect(reset(1439)).toBe("23 小时 59 分后重置 · 10月1日 周四 9:39");
  expect(reset(60)).toBe("1 小时 0 分后重置 · 今天 10:40");
  expect(reset(59)).toBe("59 分钟后重置 · 今天 10:39");
  expect(quotaRow(create(RateLimitSchema, { usedPercent: 10, resetsAt: BigInt(Math.floor(now / 1000) + 30) }), message("quota.days", { count: 7 }), now, true).reset).toBe("即将重置 · 今天 9:40");
});

it("ages online readings at 30 minutes, suppresses current warnings, and dates earlier observations", () => {
  const now = Date.parse("2026-11-12T16:16:00+08:00");
  const q = create(AgentQuotaSchema, { agent: Agent.CLAUDE, state: QuotaState.OK, observedMs: BigInt(now), windows: [{ name: "seven_day", usedPercent: 97, windowMinutes: 10080n, resetsAtMs: BigInt(now + 86_400_000) }] });
  const view = () => quotaViews([q], [Agent.CLAUDE], now, true)[0];
  q.observedMs = BigInt(now - 1_799_999);
  expect(view().rows[0].tone).toBe("warn");
  q.observedMs = BigInt(now - 1_800_000);
  expect(view()).toMatchObject({ stale: true, freshness: "30 分钟前的数据，可能已变化", banner: "" });
  expect(view().rows[0]).toMatchObject({ tone: "stale", left: "上次剩 3%" });
  q.observedMs = BigInt(now - 14 * 3_600_000);
  expect(view().freshness).toBe("14 小时前的数据，可能已变化");
  q.observedMs = BigInt(Date.parse("2026-11-11T23:59:00+08:00"));
  expect(view().seen).toBe("最近收到 11月11日 23:59");
  q.observedMs = 0n;
  expect(view()).toMatchObject({ stale: true, freshness: message("quota.unknown_time"), seen: "" });
});

it("matches the screenshot reset arithmetic and Beijing weekdays", () => {
  const now = Date.parse("2026-11-12T16:16:00+08:00");
  for (const [reset, expected] of [
    ["2026-11-18T17:30:00+08:00", "6 天 1 小时后重置 · 11月18日 周三 17:30"],
    ["2026-11-12T17:30:00+08:00", "1 小时 14 分后重置 · 今天 17:30"],
    ["2026-11-14T01:00:00+08:00", "1 天 8 小时后重置 · 11月14日 周六 1:00"],
  ]) {
    expect(quotaRow(create(RateLimitSchema, { usedPercent: 29, resetsAt: BigInt(Date.parse(reset) / 1000) }), message("quota.days", { count: 7 }), now, true).reset).toBe(expected);
  }
});
