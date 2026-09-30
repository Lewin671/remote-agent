import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import { RateLimitSchema } from "./gen/remoteagent/v1/event_pb";
import { quotaRow } from "./quota";

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
