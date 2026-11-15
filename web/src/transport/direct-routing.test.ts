import { expect, it } from "vitest";
import { DirectRouting } from "./direct-routing";

it("requires two actual 4 MiB comparisons from the same file before bulk upgrade", () => {
  let now = 0; const routing = new DirectRouting(() => now), hint = { kind: "bulk", file: "file-a" } as const;
  expect(routing.choose(hint, true)).toBe("relay");
  for (let round = 0; round < 2; round++) {
    routing.observe(hint, "relay", 4 << 20, 1000); expect(routing.choose(hint, true)).toBe("direct");
    routing.observe(hint, "direct", 4 << 20, 1000 / .96); now += 2000;
    expect(routing.choose(hint, true)).toBe(round ? "direct" : "relay");
  }
  expect(routing.choose({ kind: "bulk", file: "file-b" }, true)).toBe("relay");
  expect(routing.choose(hint, false)).toBe("relay");
});
it("does not replace throughput with RTT and holds five seconds after a hard fault", () => {
  let now = 0; const routing = new DirectRouting(() => now), hint = { kind: "bulk", file: "file" } as const;
  routing.observe(hint, "relay", 4 << 20, 1000); routing.observe(hint, "direct", 4 << 20, 2000);
  expect(routing.choose(hint, true)).toBe("relay");
  routing.fail(hint); routing.observe(hint, "relay", 4 << 20, 1000);
  now = 4999; expect(routing.choose(hint, true)).toBe("relay");
  now = 5000; expect(routing.choose(hint, true)).toBe("direct");
});
it("keeps read preference behind duration and sample requirements", () => {
  let now = 0; const routing = new DirectRouting(() => now), hint = { kind: "read" } as const;
  for (let i = 0; i < 100; i++) { now = i * 50; routing.observe(hint, "relay", 1, 150); routing.observe(hint, "direct", 1, 40); }
  expect(routing.choose(hint, true)).toBe("relay");
  now = 5000; routing.observe(hint, "direct", 1, 40);
  expect(routing.choose(hint, true)).toBe("direct"); expect(routing.choose(hint, true)).toBe("direct");
});
