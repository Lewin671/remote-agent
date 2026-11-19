import { create } from "@bufbuild/protobuf";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import {
  UsageAgentSourceSchema,
  UsageBucketGranularity,
  UsageBucketPointSchema,
  UsagePeriodSummarySchema,
  UsageRangePreset,
  UsageSeriesSchema,
  UsageSourceState,
  UsageStatsResponseSchema,
  type GetUsageStatsRequest,
  type UsageBucketPoint,
  type UsageSeries,
  type UsageStatsResponse,
} from "../gen/remoteagent/v1/local_pb";

function defaultBounds(preset: UsageRangePreset, nowMs: number): { bounds: bigint[]; gran: UsageBucketGranularity } {
  const now = new Date(nowMs);
  const todayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const tomorrowStart = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1);
  if (preset === UsageRangePreset.TODAY) {
    const bounds: bigint[] = [];
    for (let t = todayStart.getTime(); t < tomorrowStart.getTime(); t += 3600_000) bounds.push(BigInt(t));
    bounds.push(BigInt(tomorrowStart.getTime()));
    return { bounds, gran: UsageBucketGranularity.HOUR };
  }
  const days = preset === UsageRangePreset.THIRTY_DAYS ? 30 : 7;
  const bounds: bigint[] = [];
  for (let i = days - 1; i >= 0; i--) {
    bounds.push(BigInt(new Date(now.getFullYear(), now.getMonth(), now.getDate() - i).getTime()));
  }
  bounds.push(BigInt(tomorrowStart.getTime()));
  return { bounds, gran: UsageBucketGranularity.DAY };
}

function summarize(buckets: UsageBucketPoint[], recentSpeedP50: number, recentSpeedCount: bigint) {
  let u = 0n;
  let r = 0n;
  let w = 0n;
  let o = 0n;
  let reas = 0n;
  let uReqs = 0n;
  let sReqs = 0n;
  const sp50: number[] = [];
  const sp10: number[] = [];
  const sp90: number[] = [];
  const tp50: number[] = [];
  const tp10: number[] = [];
  const tp90: number[] = [];
  for (const b of buckets) {
    u += b.uncachedInputTokens;
    r += b.cacheReadTokens;
    w += b.cacheWriteTokens;
    o += b.outputTokens;
    reas += b.reasoningOutputTokens;
    uReqs += b.usageRequestCount;
    sReqs += b.speedRequestCount;
    if (b.speedRequestCount > 0n && b.speedP50 > 0) {
      sp10.push(b.speedP10 || b.speedP50 * 0.72);
      sp50.push(b.speedP50);
      sp90.push(b.speedP90 || b.speedP50 * 1.28);
    }
    if (b.speedRequestCount > 0n && b.ttftP50Ms > 0) {
      tp10.push(b.ttftP10Ms || b.ttftP50Ms * 0.68);
      tp50.push(b.ttftP50Ms);
      tp90.push(b.ttftP90Ms || b.ttftP50Ms * 1.55);
    }
  }
  const avg = (arr: number[]) => (arr.length ? arr.reduce((a, b) => a + b, 0) / arr.length : 0);
  const inDenom = u + r + w;
  return create(UsagePeriodSummarySchema, {
    uncachedInputTokens: u,
    cacheReadTokens: r,
    cacheWriteTokens: w,
    outputTokens: o,
    reasoningOutputTokens: reas,
    usageRequestCount: uReqs,
    speedRequestCount: sReqs,
    hasCacheHitRate: inDenom > 0n,
    cacheHitRate: inDenom > 0n ? Number(r) / Number(inDenom) : 0,
    hasSpeed: sp50.length > 0,
    speedP10: sp10.length ? Math.min(...sp10) : 0,
    speedP50: avg(sp50),
    speedP90: sp90.length ? Math.max(...sp90) : 0,
    hasTtft: tp50.length > 0,
    ttftP10Ms: tp10.length ? Math.min(...tp10) : 0,
    ttftP50Ms: avg(tp50),
    ttftP90Ms: tp90.length ? Math.max(...tp90) : 0,
    recent10SpeedP50: recentSpeedP50,
    recent10SpeedCount: recentSpeedCount,
  });
}

function makeSeries(
  bounds: bigint[],
  opts: {
    agent: Agent;
    model: string;
    cacheWriteSupported: boolean;
    skipBuckets?: Set<number>;
    mult?: number;
    baseUncached: number;
    baseRead: number;
    baseWrite: number;
    baseOut: number;
    baseReas?: number;
    baseReqs: number;
    speedP50: number;
    ttftP50Ms: number;
    prevRatio?: number;
    prevSpeedRatio?: number;
    prevTtftRatio?: number;
    hitDeltaAdjust?: number;
    outlierIdx?: number;
  },
): UsageSeries {
  const n = Math.max(1, bounds.length - 1);
  const mult = opts.mult ?? 1;
  const buckets: UsageBucketPoint[] = [];
  for (let i = 0; i < n; i++) {
    const sMs = bounds[i];
    const eMs = bounds[i + 1];
    if (opts.skipBuckets?.has(i)) {
      buckets.push(create(UsageBucketPointSchema, { startMs: sMs, endMs: eMs }));
      continue;
    }
    const wave = 0.72 + ((i * 37) % 11) * 0.06;
    const reqs = Math.max(1, Math.round(opts.baseReqs * wave));
    const speedReqs = opts.speedP50 > 0 ? Math.max(1, Math.round(reqs * 0.85)) : 0;
    const u = BigInt(Math.round(opts.baseUncached * mult * wave));
    const r = BigInt(Math.round(opts.baseRead * mult * wave));
    const w = opts.cacheWriteSupported ? BigInt(Math.round(opts.baseWrite * mult * wave)) : 0n;
    const o = BigInt(Math.round(opts.baseOut * mult * wave));
    const reas = opts.baseReas ? BigInt(Math.round(opts.baseReas * mult * wave)) : 0n;
    const sp = opts.speedP50 > 0 ? opts.speedP50 * (0.9 + ((i * 19) % 7) * 0.03) : 0;
    const sp90 = opts.outlierIdx === i && sp > 0 ? sp * 4.8 : sp * 1.26;
    const tt = opts.ttftP50Ms > 0 ? opts.ttftP50Ms * (0.86 + ((i * 23) % 9) * 0.04) : 0;
    buckets.push(
      create(UsageBucketPointSchema, {
        startMs: sMs,
        endMs: eMs,
        uncachedInputTokens: u,
        cacheReadTokens: r,
        cacheWriteTokens: w,
        outputTokens: o,
        reasoningOutputTokens: reas,
        usageRequestCount: BigInt(reqs),
        speedRequestCount: BigInt(speedReqs),
        speedP10: sp > 0 ? sp * 0.72 : 0,
        speedP50: sp,
        speedP90: sp90,
        ttftP10Ms: tt > 0 ? tt * 0.65 : 0,
        ttftP50Ms: tt,
        ttftP90Ms: tt > 0 ? tt * 1.68 : 0,
      }),
    );
  }
  const current = summarize(buckets, opts.speedP50, opts.speedP50 > 0 ? 10n : 0n);
  const pr = opts.prevRatio ?? 0.84;
  const pSr = opts.prevSpeedRatio ?? 0.94;
  const pTr = opts.prevTtftRatio ?? 1.12;
  const prevU = BigInt(Math.round(Number(current.uncachedInputTokens) * pr));
  const prevR = BigInt(Math.round(Number(current.cacheReadTokens) * pr));
  const prevW = BigInt(Math.round(Number(current.cacheWriteTokens) * pr));
  const prevO = BigInt(Math.round(Number(current.outputTokens) * pr));
  const prevDen = prevU + prevR + prevW;
  const previous = create(UsagePeriodSummarySchema, {
    uncachedInputTokens: prevU,
    cacheReadTokens: prevR,
    cacheWriteTokens: prevW,
    outputTokens: prevO,
    usageRequestCount: BigInt(Math.max(4, Math.round(Number(current.usageRequestCount) * pr))),
    speedRequestCount: BigInt(current.speedRequestCount > 0n ? Math.max(3, Math.round(Number(current.speedRequestCount) * pr)) : 0),
    hasCacheHitRate: prevDen > 0n,
    cacheHitRate: Math.min(1, Math.max(0, current.cacheHitRate - (opts.hitDeltaAdjust ?? 0.04))),
    hasSpeed: current.hasSpeed,
    speedP10: current.speedP10 * pSr,
    speedP50: current.speedP50 * pSr,
    speedP90: current.speedP90 * pSr,
    hasTtft: current.hasTtft,
    ttftP10Ms: current.ttftP10Ms * pTr,
    ttftP50Ms: current.ttftP50Ms * pTr,
    ttftP90Ms: current.ttftP90Ms * pTr,
  });
  return create(UsageSeriesSchema, {
    agent: opts.agent,
    model: opts.model,
    cacheWriteSupported: opts.cacheWriteSupported,
    current,
    previous,
    buckets,
  });
}

export function buildDemoUsageResponse(
  opts: URLSearchParams,
  disabledAgents: readonly Agent[],
  req?: GetUsageStatsRequest,
  nowMs = Date.now(),
): UsageStatsResponse {
  const mode = opts.get("usage") ?? "normal";
  const preset = !req?.preset ? UsageRangePreset.SEVEN_DAYS : req.preset;
  let gran = UsageBucketGranularity.DAY;
  let bounds: bigint[];
  if (req?.bucketBoundsMs && req.bucketBoundsMs.length >= 2) {
    bounds = [...req.bucketBoundsMs];
    const span = Number(bounds[bounds.length - 1] - bounds[0]) / (bounds.length - 1);
    gran = span <= 2 * 3600_000 ? UsageBucketGranularity.HOUR : span <= 12 * 3600_000 ? UsageBucketGranularity.SIX_HOURS : UsageBucketGranularity.DAY;
  } else {
    const def = defaultBounds(preset, nowMs);
    bounds = def.bounds;
    gran = def.gran;
  }

  const disabled = new Set<Agent>(disabledAgents);
  const sources = [Agent.CODEX, Agent.CLAUDE, Agent.ANTIGRAVITY]
    .filter((a) => !disabled.has(a))
    .map((a) =>
      create(UsageAgentSourceSchema, {
        agent: a,
        state: a === Agent.ANTIGRAVITY ? UsageSourceState.UNAVAILABLE : UsageSourceState.OK,
      }),
    );

  const scaleMult = preset === UsageRangePreset.TODAY ? 0.25 : preset === UsageRangePreset.THIRTY_DAYS ? 1.4 : 1;
  let series: UsageSeries[] = [];

  if (mode === "empty") {
    series = [];
  } else if (mode === "few") {
    const n = Math.max(2, bounds.length - 1);
    const skip = new Set<number>();
    for (let i = 0; i < n; i++) {
      if (i !== 1 && i !== n - 2) skip.add(i);
    }
    if (!disabled.has(Agent.CODEX)) {
      series.push(
        makeSeries(bounds, {
          agent: Agent.CODEX,
          model: "gpt-6-astra",
          cacheWriteSupported: false,
          skipBuckets: skip,
          baseUncached: 6200,
          baseRead: 54000,
          baseWrite: 0,
          baseOut: 1800,
          baseReqs: 1,
          speedP50: 84,
          ttftP50Ms: 540,
        }),
      );
      // Force exact < 3 sample count for visual check
      if (series[0].current) {
        series[0].current.usageRequestCount = 2n;
        series[0].current.speedRequestCount = 2n;
      }
    }
  } else if (mode === "extreme") {
    if (!disabled.has(Agent.CODEX)) {
      series.push(
        makeSeries(bounds, {
          agent: Agent.CODEX,
          model: "GPT-6-Flash-Speculative-202611",
          cacheWriteSupported: false,
          mult: scaleMult,
          baseUncached: 34_000_000,
          baseRead: 430_000_000,
          baseWrite: 0,
          baseOut: 4_800_000,
          baseReqs: 420,
          speedP50: 2000,
          ttftP50Ms: 210,
          prevRatio: 0.66,
        }),
      );
      series.push(
        makeSeries(bounds, {
          agent: Agent.CODEX,
          model: "Deep-Research-Pro-Slow",
          cacheWriteSupported: false,
          mult: scaleMult,
          baseUncached: 120,
          baseRead: 6_900_000,
          baseWrite: 0,
          baseOut: 900_000,
          baseReqs: 9,
          speedP50: 0.4,
          ttftP50Ms: 14_200,
          hitDeltaAdjust: 0.01,
        }),
      );
    }
    if (!disabled.has(Agent.CLAUDE)) {
      series.push(
        makeSeries(bounds, {
          agent: Agent.CLAUDE,
          model: "Claude-Opus-5.5-20261101-Thinking",
          cacheWriteSupported: true,
          mult: scaleMult,
          baseUncached: 42_000_000,
          baseRead: 262_000_000,
          baseWrite: 18_000_000,
          baseOut: 12_000_000,
          baseReas: 6_000_000,
          baseReqs: 310,
          speedP50: 96,
          ttftP50Ms: 920,
          outlierIdx: Math.min(2, bounds.length - 2),
        }),
      );
    }
  } else {
    // normal or backfilling
    if (!disabled.has(Agent.CODEX)) {
      series.push(
        makeSeries(bounds, {
          agent: Agent.CODEX,
          model: "gpt-6-astra",
          cacheWriteSupported: false,
          skipBuckets: bounds.length > 5 ? new Set([2]) : undefined,
          mult: scaleMult,
          baseUncached: 280_000,
          baseRead: 2_400_000,
          baseWrite: 0,
          baseOut: 78_000,
          baseReqs: 32,
          speedP50: 118,
          ttftP50Ms: 620,
          prevRatio: 0.85,
          prevSpeedRatio: 0.93,
          prevTtftRatio: 1.15,
          outlierIdx: bounds.length > 4 ? 3 : undefined,
        }),
      );
    }
    if (!disabled.has(Agent.CLAUDE)) {
      series.push(
        makeSeries(bounds, {
          agent: Agent.CLAUDE,
          model: "claude-opus-5-5",
          cacheWriteSupported: true,
          mult: scaleMult,
          baseUncached: 62_000,
          baseRead: 490_000,
          baseWrite: 54_000,
          baseOut: 22_000,
          baseReas: 6_200,
          baseReqs: 14,
          speedP50: 84,
          ttftP50Ms: 890,
          prevRatio: 0.91,
          prevTtftRatio: 0.96,
        }),
      );
    }
    if (!disabled.has(Agent.CODEX)) {
      series.push(
        makeSeries(bounds, {
          agent: Agent.CODEX,
          model: "gpt-5.4-mini",
          cacheWriteSupported: false,
          mult: scaleMult,
          baseUncached: 34_000,
          baseRead: 165_000,
          baseWrite: 0,
          baseOut: 19_000,
          baseReqs: 9,
          speedP50: 164,
          ttftP50Ms: 410,
          prevRatio: 1.04,
          prevTtftRatio: 1.01,
        }),
      );
    }
    if (!disabled.has(Agent.CLAUDE)) {
      series.push(
        makeSeries(bounds, {
          agent: Agent.CLAUDE,
          model: "claude-sonnet-5-5",
          cacheWriteSupported: true,
          mult: scaleMult,
          baseUncached: 22_000,
          baseRead: 98_000,
          baseWrite: 14_000,
          baseOut: 9_000,
          baseReqs: 6,
          speedP50: 102,
          ttftP50Ms: 680,
          prevRatio: 0.96,
        }),
      );
    }
    if (!disabled.has(Agent.CODEX)) {
      series.push(
        makeSeries(bounds, {
          agent: Agent.CODEX,
          model: "gpt-5.3-legacy",
          cacheWriteSupported: false,
          mult: scaleMult,
          baseUncached: 8_000,
          baseRead: 24_000,
          baseWrite: 0,
          baseOut: 3_200,
          baseReqs: 4,
          speedP50: 66,
          ttftP50Ms: 1_240,
          prevRatio: 1.12,
        }),
      );
    }
  }

  return create(UsageStatsResponseSchema, {
    preset,
    granularity: gran,
    startMs: bounds[0],
    endMs: bounds[bounds.length - 1],
    generatedAtMs: BigInt(nowMs),
    backfilling: mode === "backfilling",
    backfillScannedDays: mode === "backfilling" ? 14 : 90,
    backfillTotalDays: 90,
    previousPeriodBackfilling: mode === "backfilling",
    sources,
    series,
  });
}
