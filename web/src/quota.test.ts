import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import { RateLimitSchema } from "./gen/remoteagent/v1/event_pb";
import { quotaRow, quotaSummary, quotaViews } from "./quota";
import { Agent } from "./gen/remoteagent/v1/event_pb";
import { AgentQuotaSchema, QuotaState, QuotaWindowSchema } from "./gen/remoteagent/v1/remote_pb";
import { describe } from "vitest";

const now = new Date(2026, 8, 30, 9, 40).getTime();
const lim = (usedPercent: number, windowMinutes: number, resetInMin: number) => create(RateLimitSchema, { usedPercent, windowMinutes: BigInt(windowMinutes), resetsAt: BigInt(Math.floor((now + resetInMin * 60_000) / 1000)) });

it("shows use and reset, warns from 80% and calls 100% used up", () => {
  expect(quotaRow(lim(62, 300, 270), "5 小时", now, true)).toMatchObject({ label: "5 小时", used: "已用 62%", reset: "14:10 重置", tone: "" });
  expect(quotaRow(lim(92, 300, 30), "5 小时", now, true).tone).toBe("warn");
  expect(quotaRow(lim(100, 10080, 3 * 1440), "7 天", now, true)).toMatchObject({ label: "7 天", used: "已用尽", tone: "full" });
});

it("never shows a missing window as 0%", () => {
  const r = quotaRow(undefined, "7 天", now, true);
  expect(r.used).toBe("未获取");
  expect(r.percent).toBeUndefined();
});

it("keeps an old reading as such: offline, or past its reset", () => {
  expect(quotaRow(lim(62, 300, 60), "5 小时", now, false)).toMatchObject({ tone: "stale", used: "已用 62%" });
  expect(quotaRow(lim(62, 300, -5), "5 小时", now, true)).toMatchObject({ tone: "stale", used: "等待更新" });
});

describe("account quota from MachineMeta.quotas (Go ed1a14d)", () => {
  const at = (h: number, m = 0) => new Date(2026, 8, 30, h, m).getTime();
  const q = (init: object) => create(AgentQuotaSchema, init as never);
  const w = (name: string, used: number | undefined, resetH: number | undefined, minutes: number) =>
    create(QuotaWindowSchema, { name, usedPercent: used, resetsAtMs: resetH === undefined ? 0n : BigInt(at(resetH, 10)), windowMinutes: BigInt(minutes) });
  const now = at(9, 41);

  it("shows each window's use, reset and when it was last heard, per CLI", () => {
    const [v] = quotaViews([q({ agent: Agent.CODEX, state: QuotaState.OK, plan: "Pro Lite", observedMs: BigInt(at(9, 40)), windows: [w("primary", 62, 14, 300), w("secondary", 34, 20, 10080)] })], [Agent.CODEX], now, true);
    expect(v.title).toBe("Codex · Pro Lite");
    expect(v.rows.map((r) => [r.label, r.used, r.reset, r.tone])).toEqual([
      ["5 小时", "已用 62%", "14:10 重置 · 最近收到 9:40", ""],
      ["7 天", "已用 34%", "20:10 重置 · 最近收到 9:40", ""],
    ]);
  });

  it("marks a missing usage 未获取, never 0%, and a missing reset time by leaving it out", () => {
    const [v] = quotaViews([q({ agent: Agent.CLAUDE, state: QuotaState.OK, observedMs: BigInt(at(9, 40)), windows: [w("five_hour", 92, 13, 300), w("seven_day", undefined, undefined, 10080)] })], [Agent.CLAUDE], now, true);
    expect(v.rows[0]).toMatchObject({ used: "已用 92%", tone: "warn" });
    expect(v.rows[1]).toMatchObject({ label: "7 天", used: "未获取", reset: "最近收到 9:40" });
    expect(v.rows[1].percent).toBeUndefined();
    expect(v.banner).toBe("5 小时额度即将用尽");
    // A window Claude Code did not report at all is written as missing too.
    const [only] = quotaViews([q({ agent: Agent.CLAUDE, state: QuotaState.OK, observedMs: BigInt(at(9, 40)), windows: [w("five_hour", 100, 13, 300)] })], [Agent.CLAUDE], now, true);
    expect(only.rows.map((r) => [r.label, r.used])).toEqual([["5 小时", "已用尽"], ["7 天", "未获取"]]);
    expect(only.note).toBe("只有 5 小时用量可用；7 天额度暂时未知。");
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
    expect(claude).toMatchObject({ kind: "waiting", head: "暂未获取额度" });
    expect(codex).toMatchObject({ kind: "failed", error: "没能读取 Codex 额度：rollout unreadable", title: "Codex · 上次记录" });
    expect(codex.rows[0]).toMatchObject({ used: "上次已用 28%", tone: "stale", reset: "13:10 重置 · 最近成功收到 9:20" });
    const [unsup] = quotaViews([q({ agent: Agent.CLAUDE, state: QuotaState.UNSUPPORTED, detail: "托管设置固定了状态栏" })], [Agent.CLAUDE], now, true);
    expect(unsup).toMatchObject({ kind: "unsupported", head: "暂不支持读取额度", detail: "托管设置固定了状态栏" });
    // A CLI the daemon has no entry for is not supported by it.
    expect(quotaViews([q({ agent: Agent.CODEX, state: QuotaState.WAITING })], [Agent.CODEX, Agent.CLAUDE], now, true).find((v) => v.agent === Agent.CLAUDE)?.kind).toBe("unsupported");
  });

  it("greys an offline machine's reading and waits for an update after a reset time passed", () => {
    const [off] = quotaViews([q({ agent: Agent.CLAUDE, state: QuotaState.OK, observedMs: BigInt(at(9, 40) - 86_400_000), windows: [w("five_hour", 92, 8, 300)] })], [Agent.CLAUDE], now, false);
    expect(off.title).toBe("Claude Code · 上次记录");
    expect(off.rows[0]).toMatchObject({ used: "等待更新", tone: "stale", note: "上次已用 92%；重置时间已过，不能据此确认额度已经恢复" });
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
    expect(quotaSummary(views.filter((v) => v.agent === Agent.CODEX))).toBe("5 小时 已用 20%");
  });
});
