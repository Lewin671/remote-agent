import { expect, test } from "vitest";
import { DIRECT_RULE, DirectRouting, type RouteHint, type RouteName } from "./direct-routing";

function makeRouting(start = 10_000) {
  let now = start;
  const route = new DirectRouting(() => now);
  return {
    route,
    get now() { return now; },
    advance(ms: number) { now += ms; },
  };
}

function observeReads(route: DirectRouting, advance: (ms: number) => void, counts: { direct: number; relay: number }, ms: { direct: number; relay: number }, step = 500) {
  for (let i = 0; i < counts.direct; i++) {
    route.observe({ kind: "read" }, "direct", 0, ms.direct);
    advance(step);
  }
  for (let i = 0; i < counts.relay; i++) {
    route.observe({ kind: "read" }, "relay", 0, ms.relay);
    advance(step);
  }
}

test("direct is preferred immediately on ready without warm-up, with 1/8 relay control sampling", () => {
  const { route } = makeRouting();
  expect(route.readPath()).toBe("direct");
  expect(route.filePath).toBe("direct");

  const readCounts = { direct: 0, relay: 0 };
  for (let i = 0; i < 64; i++) readCounts[route.choose({ kind: "read" }, true)]++;
  expect(readCounts).toEqual({ direct: 56, relay: 8 });

  const bulkCounts = { direct: 0, relay: 0 };
  for (let i = 0; i < 64; i++) bulkCounts[route.choose({ kind: "bulk", file: "f" }, true)]++;
  expect(bulkCounts).toEqual({ direct: 56, relay: 8 });

  expect(route.choose({ kind: "read" }, false)).toBe("relay");
  expect(route.choose({ kind: "bulk", file: "f" }, false)).toBe("relay");
});

test.each([
  { name: "insufficient direct samples (7/4)", directN: 7, relayN: 4, directMs: 200, relayMs: 40, expectPath: "direct" as RouteName },
  { name: "insufficient relay samples (8/3)", directN: 8, relayN: 3, directMs: 200, relayMs: 40, expectPath: "direct" as RouteName },
  { name: "within 1.5x + 20ms margin (80 vs 40 -> 80 <= 80)", directN: 8, relayN: 4, directMs: 80, relayMs: 40, expectPath: "direct" as RouteName },
  { name: "strictly slower than 1.5x + 20ms (81 vs 40 -> 81 > 80)", directN: 8, relayN: 4, directMs: 81, relayMs: 40, expectPath: "relay" as RouteName },
])("read fallback rule: $name", ({ directN, relayN, directMs, relayMs, expectPath }) => {
  const sim = makeRouting();
  observeReads(sim.route, (ms) => sim.advance(ms), { direct: directN, relay: relayN }, { direct: directMs, relay: relayMs });
  expect(sim.route.readPath()).toBe(expectPath);
  expect(sim.route.readSlowFallback()).toBe(expectPath === "relay");
  expect(sim.route.filePath).toBe("direct");
});

test("read fallback ignores samples older than 30s", () => {
  const sim = makeRouting();
  observeReads(sim.route, (ms) => sim.advance(ms), { direct: 4, relay: 4 }, { direct: 500, relay: 20 });
  expect(sim.route.readPath()).toBe("direct");

  sim.advance(DIRECT_RULE.read.windowMs + 1);
  // Old direct samples expired; only 4 fresh direct + 4 fresh relay remain.
  observeReads(sim.route, (ms) => sim.advance(ms), { direct: 4, relay: 4 }, { direct: 500, relay: 20 });
  expect(sim.route.readPath()).toBe("direct");

  observeReads(sim.route, (ms) => sim.advance(ms), { direct: 4, relay: 0 }, { direct: 500, relay: 20 });
  expect(sim.route.readPath()).toBe("relay");
});

test.each([
  { name: "direct >= 70% of relay stays direct (2.8 MiB/s vs 4.0 MiB/s)", directMiBps: 2.8, relayMiBps: 4.0, expectPath: "direct" as RouteName },
  { name: "direct < 70% of relay falls back (2.7 MiB/s vs 4.0 MiB/s)", directMiBps: 2.7, relayMiBps: 4.0, expectPath: "relay" as RouteName },
])("bulk segment fallback rule: $name", ({ directMiBps, relayMiBps, expectPath }) => {
  const sim = makeRouting();
  const hint: RouteHint = { kind: "bulk", file: "archive.tar" };
  sim.route.observe(hint, "relay", relayMiBps * (1 << 20), 1000);
  sim.route.observe(hint, "direct", directMiBps * (1 << 20), 1000);
  expect(sim.route.filePath).toBe(expectPath);
  expect(sim.route.fileSlowFallback()).toBe(expectPath === "relay");
  expect(sim.route.readPath()).toBe("direct");
});

test("slow fallback backs off 60s -> 120s -> 240s -> 480s -> 600s and resets on network change or rebuild", () => {
  const sim = makeRouting();
  const triggerSlowRead = () => {
    observeReads(sim.route, (ms) => sim.advance(ms), { direct: 8, relay: 3 }, { direct: 250, relay: 30 }, 100);
    sim.route.observe({ kind: "read" }, "relay", 0, 30);
  };
  const triggerSlowBulk = () => {
    const f: RouteHint = { kind: "bulk", file: `f-${sim.now}` };
    sim.route.observe(f, "relay", 8 << 20, 1000);
    sim.route.observe(f, "direct", 2 << 20, 1000);
  };

  for (const expectedWait of [60_000, 120_000, 240_000, 480_000, 600_000, 600_000]) {
    triggerSlowRead();
    expect(sim.route.readPath()).toBe("relay");
    sim.advance(expectedWait - 1);
    expect(sim.route.readPath()).toBe("relay");
    sim.advance(1);
    expect(sim.route.readPath()).toBe("direct");
  }

  // Re-prefer stays direct even before new samples prove otherwise (no oscillation).
  expect(sim.route.choose({ kind: "read" }, true)).toBe("direct");

  // Rebuilding direct path resets read backoff back to 60s on next fallback.
  sim.route.reset("read");
  triggerSlowRead();
  expect(sim.route.readPath()).toBe("relay");
  sim.advance(59_999);
  expect(sim.route.readPath()).toBe("relay");
  sim.advance(1);
  expect(sim.route.readPath()).toBe("direct");

  // Bulk has independent backoff and resets on full network reset().
  triggerSlowBulk();
  expect(sim.route.filePath).toBe("relay");
  sim.advance(60_000);
  expect(sim.route.filePath).toBe("direct");
  triggerSlowBulk();
  expect(sim.route.filePath).toBe("relay");
  sim.route.reset();
  expect(sim.route.filePath).toBe("direct");
  triggerSlowBulk();
  sim.advance(60_000);
  expect(sim.route.filePath).toBe("direct");
});

test("measured values require 3 samples/60s and clear on slow fallback or hardFail", () => {
  const sim = makeRouting();
  for (let i = 0; i < 2; i++) {
    sim.route.observe({ kind: "read" }, "direct", 0, 18);
    sim.route.observe({ kind: "read" }, "relay", 0, 150);
  }
  expect(sim.route.measured().readMs).toEqual({});

  sim.route.observe({ kind: "read" }, "direct", 0, 18);
  sim.route.observe({ kind: "read" }, "relay", 0, 150);
  expect(sim.route.measured().readMs).toEqual({ direct: 18, relay: 150 });

  sim.route.fail({ kind: "read" });
  expect(sim.route.readSlowFallback()).toBe(true);
  expect(sim.route.measured().readMs).toEqual({});

  sim.route.observe({ kind: "bulk", file: "f" }, "direct", 4 << 20, 500);
  sim.route.observe({ kind: "bulk", file: "f" }, "relay", 4 << 20, 1000);
  expect(sim.route.measured().fileMiBps).toEqual({ direct: 8, relay: 4 });
  sim.route.hardFail("bulk");
  expect(sim.route.measured().fileMiBps).toEqual({});
});
