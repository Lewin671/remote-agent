import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { afterEach, beforeEach, expect, it } from "vitest";
import { MachineStatusSchema, ResourcePressure, ResourceUsageSchema } from "./gen/remoteagent/v1/local_pb";
import { localeStore } from "./i18n";
import {
  DOT_INSET, WINDOW_MS, memText, barPercent, batteryText, curvePaths, curvePoints, diskBar, ChartTimeline, footerText, gbFixed, gbNumber, pressurePosition,
  ringsOf, sampleAt, secondsBefore, sizeText, staleText, toneOf, uptimeText, visiblePoints, xOf, type Point,
} from "./machine-status-view";

beforeEach(() => localeStore.overrideForTesting("zh-CN"));
afterEach(() => localeStore.overrideForTesting(undefined));

const status = (init: MessageInitShape<typeof MachineStatusSchema> = {}) => create(MachineStatusSchema, init);

it("writes sizes in decimal units: one decimal below 10 GB, none from 10, MB below a GB", () => {
  expect(gbNumber(42_000_000_000n)).toBe("42");
  expect(gbNumber(1_200_000_000n)).toBe("1.2");
  expect(gbNumber(16_000_000_000n)).toBe("16");
  expect(gbNumber(8_000_000_000n)).toBe("8");
  expect(sizeText(3_100_000_000n)).toBe("3.1 GB");
  expect(sizeText(380_000_000n)).toBe("380 MB");
  expect(sizeText(256_000_000n)).toBe("256 MB");
});

it("counts memory in binary units written GB and MB, and the disk in decimal", () => {
  expect(memText(16n * 2n ** 30n)).toBe("16 GB");
  expect(memText(BigInt(Math.round(2.4 * 2 ** 30)))).toBe("2.4 GB");
  expect(memText(BigInt(Math.round(11.2 * 2 ** 30)))).toBe("11 GB");
  expect(memText(380n * 2n ** 20n)).toBe("380 MB");
  expect(gbFixed(BigInt(Math.round(11.2 * 2 ** 30)))).toBe("11.2");
  expect(sizeText(460_000_000_000n)).toBe("460 GB");
});

it("writes the same in English with the same units", () => {
  localeStore.overrideForTesting("en");
  expect(sizeText(3_100_000_000n)).toBe("3.1 GB");
  expect(staleText(125_000)).toBe("Data from 2 min ago");
  expect(uptimeText(3 * 86400 + 4 * 3600)).toBe("Up 3 d 4 hr");
});

it("draws the disk ring as what is used and writes what is free; colours only memory and disk", () => {
  const rings = ringsOf(status({
    cpuPercent: 23,
    memoryUsedBytes: 11_200_000_000n,
    memoryTotalBytes: 16_000_000_000n,
    memoryPressure: ResourcePressure.WARNING,
    disk: { totalBytes: 460_000_000_000n, availableBytes: 1_200_000_000n, pressure: ResourcePressure.CRITICAL },
  }));
  expect(rings.map((r) => r.text)).toEqual(["23%", "70%", "1.2 GB"]);
  expect(rings[2].ratio).toBeCloseTo(1 - 1.2 / 460, 5);
  expect(rings.map((r) => r.tone)).toEqual(["", "warn", "crit"]);
  expect(toneOf(ResourcePressure.NORMAL)).toBe("");
});

it("splits the disk bar into remote-agent's use and the rest, leaving the free space", () => {
  const bar = diskBar(status({ disk: { totalBytes: 460_000_000_000n, availableBytes: 42_000_000_000n } }).disk!, 3_100_000_000n);
  expect(bar.agent).toBeCloseTo(0.674, 2);
  expect(bar.agent + bar.other).toBeCloseTo(((460 - 42) / 460) * 100, 5);
});

it("words how old stale numbers are, and the footer's uptime and battery", () => {
  expect(staleText(8_000)).toBe("8 秒前的数据");
  expect(staleText(125_000)).toBe("2 分钟前的数据");
  expect(uptimeText(3 * 86400 + 4 * 3600)).toBe("已开机 3 天 4 小时");
  expect(uptimeText(5 * 3600 + 7 * 60)).toBe("已开机 5 小时 7 分");
  expect(batteryText(81, true)).toBe("电池 81%，充电中");
  expect(footerText(status({ uptimeSeconds: BigInt(3 * 86400 + 4 * 3600) }))).toBe("已开机 3 天 4 小时");
  expect(footerText(status({ uptimeSeconds: BigInt(3 * 86400 + 4 * 3600), battery: { percent: 81, charging: false } }))).toBe("已开机 3 天 4 小时 · 电池 81%");
});

it("hides the pressure dot when the system gives no pressure", () => {
  expect(pressurePosition(status({ memoryPressure: ResourcePressure.UNSPECIFIED, memoryPressurePosition: 0.3 }))).toBeUndefined();
  expect(pressurePosition(status({ memoryPressure: ResourcePressure.NORMAL, memoryPressurePosition: 0.34 }))).toBe(0.34);
});

it("sizes a ranking bar against the first row, the first being full", () => {
  const a = create(ResourceUsageSchema, { id: "a", cpuPercent: 141, memoryBytes: 2_400_000_000n });
  const b = create(ResourceUsageSchema, { id: "b", cpuPercent: 62, memoryBytes: 1_100_000_000n });
  expect(barPercent(a, a, false)).toBe(100);
  expect(barPercent(b, a, false)).toBeCloseTo(43.97, 1);
  expect(barPercent(b, a, true)).toBeCloseTo(45.83, 1);
});

const at = (s: number, v = 10): Point => ({ t: 1_000_000 + s * 1000, v });

it("lays points out by their own times and breaks the line where nobody sampled", () => {
  const points = [at(0), at(2), at(4), at(60), at(62), at(64)];
  const now = points.at(-1)!.t;
  const { line, area } = curvePaths(points.map((p, i) => ({ ...p, v: 10 + i * 5 })), now, 100, 358, 104);
  // Two runs of a line, two of a fill: no stroke between 4s and 60s.
  expect(line.match(/M/g)).toHaveLength(2);
  expect(area.match(/M/g)).toHaveLength(2);
  expect(xOf(now - WINDOW_MS, now, 358)).toBe(0);
  expect(xOf(now, now, 358)).toBe(358 - DOT_INSET);
  expect(xOf(now - WINDOW_MS / 2, now, 358)).toBeCloseTo((358 - DOT_INSET) / 2, 5);
  // The full buffer spans 118 s; a 2 s lag still leaves the 116 s window on data.
  expect(WINDOW_MS).toBe(116_000);
  for (const lag of [0, 1000, 2000]) expect(118_000 - lag - WINDOW_MS).toBeGreaterThanOrEqual(0);
});

it("adds no point of its own: one point is a dot without a line", () => {
  const one = curvePaths([at(0)], at(0).t, 100, 358, 104);
  expect(one.line).toBe("");
  expect(one.head?.x).toBe(358 - DOT_INSET);
  const first = curvePoints(status({ observedMs: 5_000n, cpuPercent: 40 }), false, 0n);
  expect(first).toEqual([{ t: 5000, v: 40 }]);
});

it("keeps only the points from where the curve restarted", () => {
  const s = status({ samples: [1, 2, 3, 4].map((n) => ({ timeMs: BigInt(n * 1000), cpuPercent: n })) });
  expect(curvePoints(s, false, 3000n).map((p) => p.v)).toEqual([3, 4]);
});

it("keeps on-time samples within two seconds, continuous and inside the data", () => {
  const clock = new ChartTimeline();
  expect(clock.advance(0, 100_000, 100_000)).toBe(100_000);
  for (let now = 0; now <= 10_000; now += 100) {
    const latest = 100_000 + Math.floor(now / 2000) * 2000;
    const end = clock.advance(now, 98_000, latest);
    expect(latest - end).toBeGreaterThanOrEqual(0);
    expect(latest - end).toBeLessThanOrEqual(2000);
    if (now > 0 && now % 2000 === 0) expect(end).toBe(latest - 2000);
  }
  const history = new ChartTimeline();
  expect(history.advance(0, 98_000, 100_000)).toBe(98_000);
  expect(history.advance(1000, 98_000, 100_000)).toBe(99_000);
  expect(visiblePoints([{ t: 98_000, v: 10 }, { t: 100_000, v: 30 }], 99_000).at(-1)?.v).toBe(20);
});

it("catches a missed poll up within 500 ms with ease-out, no jump or rewind", () => {
  const clock = new ChartTimeline();
  clock.advance(0, 98_000, 100_000);
  expect(clock.advance(2000, 98_000, 100_000)).toBe(100_000);
  // The 102 s poll failed; the next snapshot includes 102 s and 104 s.
  expect(clock.advance(4000, 98_000, 104_000)).toBe(100_000);
  const frames = [0, 100, 250, 400, 500].map(dt => clock.advance(4000 + dt, 98_000, 104_000));
  expect(frames).toEqual([100_000, 101_952, 103_500, 103_968, 104_000]);
  const speeds = frames.slice(1).map((v, i) => (v - frames[i]) / [100, 150, 150, 100][i]);
  expect(speeds.every((v, i) => v > 0 && (i === 0 || v < speeds[i - 1]))).toBe(true);
  expect(clock.advance(6000, 98_000, 106_000)).toBe(104_000);
  expect(clock.advance(7000, 98_000, 106_000)).toBe(105_000);
});

it("retargets catch-up without resetting its deadline, and honours reduced motion", () => {
  const clock = new ChartTimeline();
  clock.advance(0, 100_000, 100_000);
  expect(clock.advance(4000, 98_000, 104_000)).toBe(100_000);
  const middle = clock.advance(4250, 98_000, 104_000);
  expect(clock.advance(4250, 98_000, 108_000)).toBe(middle);
  expect(clock.advance(4500, 98_000, 108_000)).toBe(108_000);
  expect(clock.advance(4516, 98_000, 106_000)).toBe(108_000);
  expect(clock.advance(4400, 98_000, 108_000)).toBe(108_000);
  expect(clock.advance(5000, 98_000, 112_000, false)).toBe(112_000);
  expect(clock.advance(5000, 98_000, 112_000, true)).toBe(112_000);
});

it("finds the sample under a position, none over a gap, and names how long before the newest it is", () => {
  const points = [at(0), at(2), at(4), at(60), at(62)];
  const now = at(62).t;
  const x = xOf(at(2).t, now, 358);
  expect(sampleAt(points, x, now, 358)?.t).toBe(at(2).t);
  expect(sampleAt(points, xOf(at(30).t, now, 358), now, 358)).toBeUndefined();
  expect(secondsBefore(at(2), at(62))).toBe(60);
  expect(secondsBefore(at(3), at(4))).toBe(2);
});
