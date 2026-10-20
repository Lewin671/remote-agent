import { Session } from "node:inspector/promises";
import { analyse } from "scslre";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { inlineSpans, mdBlocks, numericColumn, tableCells } from "./markdown";

// Charge native scans/copies as well as V8 JS execution blocks. Plain wrappers
// avoid recording every call/argument in Vitest mock histories. Only synchronous
// parser execution is intercepted; restore builtins before inspector/assertions.
function nativeWork(run: () => unknown, patterns: Set<RegExp>): number {
  let native = 0;
  const exec = RegExp.prototype.exec;
  const indexOf = String.prototype.indexOf;
  const slice = String.prototype.slice;
  const arraySlice = Array.prototype.slice;
  try {
    RegExp.prototype.exec = function (input: string) {
      const start = this.global || this.sticky ? this.lastIndex : 0;
      const match = exec.call(this, input);
      native += Math.max(0, (match ? match.index + match[0].length : input.length) - start) + 1;
      if (input.length > 64) patterns.add(this);
      return match;
    };
    String.prototype.indexOf = function (search: string, from?: number) {
      const result = indexOf.call(this, search, from);
      native += Math.max(0, (result < 0 ? this.length : result + search.length) - Math.max(0, from ?? 0)) + 1;
      return result;
    };
    String.prototype.slice = function (start?: number, end?: number) {
      const result = slice.call(this, start, end);
      native += result.length;
      return result;
    };
    Array.prototype.slice = function (start?: number, end?: number) {
      const result = arraySlice.call(this, start, end);
      native += result.length;
      return result;
    };
    run();
    return native;
  } finally {
    RegExp.prototype.exec = exec;
    String.prototype.indexOf = indexOf;
    String.prototype.slice = slice;
    Array.prototype.slice = arraySlice;
  }
}

async function work(session: Session, run: () => unknown): Promise<number> {
  // takePreciseCoverage resets counters: discard activity between measurements.
  // The complexity project disables optimizing JIT tiers: optimized/inlined
  // helpers can disappear from V8 invocation counts even in precise coverage.
  await session.post("Profiler.takePreciseCoverage");
  const native = nativeWork(run, new Set());
  const { result } = await session.post("Profiler.takePreciseCoverage");
  const parser = result.filter((script) => script.url.endsWith("/markdown.tsx"));
  expect(parser).toHaveLength(1);
  const blocks = parser.flatMap((script) => script.functions.flatMap((fn) => fn.ranges));
  const executed = blocks.reduce((sum, range) => sum + range.count, 0);
  expect(executed).toBeGreaterThan(0);
  return executed + native;
}

const inlineCases: Record<string, (n: number) => string> = {
  "unclosed brackets": (n) => "[".repeat(n),
  "unclosed targets": (n) => "[x](".repeat(n),
  "unclosed autolinks": (n) => "<https://".repeat(n),
  underscores: (n) => "_a ".repeat(n),
  strikes: (n) => "~~a ".repeat(n),
  stars: (n) => "*a ".repeat(n),
  "opening URL parentheses": (n) => "https://a.b/" + "(".repeat(n),
  "closing URL parentheses": (n) => "https://a.b/" + ")".repeat(n),
  "URLs and punctuation": (n) => "https://a.b ".repeat(n) + ".".repeat(n * 2),
  "unclosed backticks": (n) => "`".repeat(n) + "x",
};
const blockCases: Record<string, (n: number) => string> = {
  hashes: (n) => "# " + "#".repeat(n) + "x",
  "spaced hashes": (n) => "# " + "# ".repeat(n) + "x",
  "blank separator": (n) => "a|b\n" + " ".repeat(n) + "x",
  "pipe separator": (n) => "a|b\n" + "| ".repeat(n) + "x",
  "list blanks": (n) => "- " + " ".repeat(n) + "x",
  "list lookahead": (n) => "- x\n\n".repeat(n),
  "indented list": (n) => "- x\n\n" + "  y\n".repeat(n),
  // `.*` rejects these line separators. A separator's \s+ must not give
  // blanks back to `.*` and try every partition of the same whitespace run.
  "task whitespace": (n) => "- [x] " + " ".repeat(n) + "task",
  "hostile heading": (n) => "# " + " ".repeat(n) + "x\u2028y",
  "hostile list": (n) => "- " + " ".repeat(n) + "x\u2029y",
};

const cases = [
    ...Object.entries(inlineCases).map(([name, input]) => ({ name, input, parse: inlineSpans })),
    ...Object.entries(blockCases).map(([name, input]) => ({ name, input, parse: mdBlocks })),
    ...["1 1", "1   x", "1a "].map((unit) => ({ name: `quantity ${unit}`, input: (n: number) => unit.repeat(n), parse: (s: string) => numericColumn([s]) })),
    { name: "table cells", input: (n: number) => "| x `a|b` ".repeat(n), parse: tableCells },
  ];

describe("linear parser work", { concurrent: false }, () => {
  const session = new Session();
  beforeAll(async () => {
    session.connect();
    await session.post("Profiler.enable");
    await session.post("Profiler.startPreciseCoverage", { callCount: true, detailed: true });
  });
  afterAll(async () => {
    try { await session.post("Profiler.stopPreciseCoverage"); }
    finally { session.disconnect(); }
  });

  it("counts every hot helper invocation and loop iteration", async () => {
    // Calibrate the profiler itself. Optimizing JIT tiers can inline step and
    // stop reporting its calls, making work ratios depend on compiler timing.
    function step(i: number) { return i + 1; }
    function loop(n: number) {
      let sum = 0;
      for (let i = 0; i < n; i++) sum += step(i);
      return sum;
    }
    const n = 100_000;
    loop(n);
    await session.post("Profiler.takePreciseCoverage");
    expect(loop(n)).toBe(n * (n + 1) / 2);
    const { result } = await session.post("Profiler.takePreciseCoverage");
    const script = result.find((s) => s.url.endsWith("/markdown.complexity.test.ts"));
    const helper = script?.functions.find((fn) => fn.functionName === "step");
    const caller = script?.functions.find((fn) => fn.functionName === "loop");
    expect(helper?.ranges[0].count).toBe(n);
    expect(caller?.isBlockCoverage).toBe(true);
    expect(caller?.ranges.some((range) => range.count === n)).toBe(true);
  });

  it.each(cases)("$name grows linearly as hostile inputs grow fourfold", async ({ name, input, parse }) => {
    let previous = 0;
    const counts = [];
    for (const n of [256, 1024, 4096]) {
      const text = input(n);
      const count = await work(session, () => parse(text));
      // A quadratic rescan grows 16x, versus the allowed 4.5x including
      // constant overhead. Keep the independent per-code-unit bound too.
      expect(count, `${name}, n=${n}`).toBeLessThan(200 * text.length + 1000);
      if (previous) expect(count, `${name}, n=${n}`).toBeLessThanOrEqual(previous * 4.5);
      previous = count;
      counts.push(count);
    }
    if (process.env.RA_MD_WORK_REPORT) console.info(name, counts);
  });
});

it("analyses every regex exercised by the full hostile-input matrix", () => {
  // Independent collection makes this test valid on its own (including -t),
  // without depending on ordering or state left by the work-count cases.
  // It runs outside the coverage suite, so scslre is not deoptimized/profiled.
  const patterns = new Set<RegExp>();
  for (const { input, parse } of cases)
    for (const n of [256, 1024, 4096]) nativeWork(() => parse(input(n)), patterns);
  const unique = new Map([...patterns].map((re) => [re.toString(), re]));
  expect(unique.size).toBeGreaterThanOrEqual(10);
  // Native regex backtracking is invisible to the JS counters. Analyse the
  // actual patterns, never copies, retaining the original scslre guarantee.
  for (const [source, re] of unique) expect(analyse(re).reports, source).toEqual([]);
});

it("detects the former overlapping-whitespace patterns without timing them", () => {
  for (const re of [/^\s{0,3}(#{1,6})\s+(.*)$/, /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/]) {
    expect(analyse(re).reports.some((report) => report.type === "Trade")).toBe(true);
  }
});

it("preserves whitespace stripping and rejected Unicode line separators", () => {
  for (const separator of ["\u2028", "\u2029"]) {
    for (const prefix of ["# ", "- "]) {
      const text = prefix + " ".repeat(100_000) + `x${separator}y`;
      expect(mdBlocks(text)).toEqual([{ kind: "text", text }]);
    }
    expect(mdBlocks(`# ${separator}title`)).toEqual([{ kind: "heading", level: 1, text: "title" }]);
    expect(mdBlocks(`- ${separator}item`)).toMatchObject([{ kind: "list", items: [{ text: "item" }] }]);
  }
  expect(mdBlocks("#   title")).toEqual([{ kind: "heading", level: 1, text: "title" }]);
  expect(mdBlocks("-   [x]   task")).toMatchObject([{ kind: "list", items: [{ text: "task", task: "done" }] }]);
});
