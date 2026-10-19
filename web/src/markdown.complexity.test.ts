import { Session } from "node:inspector/promises";
import { analyse } from "scslre";
import { expect, it, vi } from "vitest";
import { inlineSpans, mdBlocks, numericColumn, tableCells } from "./markdown";

// Count executed JS blocks in the actual parser, including loop iterations,
// without adding counters to the shipped code. Also charge native regex scans
// and copies: V8 coverage alone would miss repeated indexOf/slice suffix scans.
// These are work units, never milliseconds. Native regex backtracking is checked
// separately below, since it is invisible to JS execution counters.
async function work(run: () => unknown, patterns: Set<RegExp>): Promise<number> {
  const session = new Session();
  session.connect();
  let native = 0;
  const exec = RegExp.prototype.exec;
  const indexOf = String.prototype.indexOf;
  const slice = String.prototype.slice;
  const arraySlice = Array.prototype.slice;
  const spies: { mockRestore(): void }[] = [];
  try {
    await session.post("Profiler.enable");
    await session.post("Profiler.startPreciseCoverage", { callCount: true, detailed: true });
    spies.push(vi.spyOn(RegExp.prototype, "exec").mockImplementation(function (this: RegExp, input: string) {
      const start = this.global || this.sticky ? this.lastIndex : 0;
      const match = exec.call(this, input);
      native += Math.max(0, (match ? match.index + match[0].length : input.length) - start) + 1;
      if (input.length > 64) patterns.add(this);
      return match;
    }));
    spies.push(vi.spyOn(String.prototype, "indexOf").mockImplementation(function (this: string, search: string, from?: number) {
      const result = indexOf.call(this, search, from);
      native += Math.max(0, (result < 0 ? this.length : result + search.length) - Math.max(0, from ?? 0)) + 1;
      return result;
    }));
    spies.push(vi.spyOn(String.prototype, "slice").mockImplementation(function (this: string, start?: number, end?: number) {
      const result = slice.call(this, start, end);
      native += result.length;
      return result;
    }));
    spies.push(vi.spyOn(Array.prototype, "slice").mockImplementation(function (this: unknown[], start?: number, end?: number) {
      const result = arraySlice.call(this, start, end);
      native += result.length;
      return result;
    }));
    run();
    // Stop intercepting builtins before the inspector/runner uses them.
    for (const spy of spies) spy.mockRestore();
    const { result } = await session.post("Profiler.takePreciseCoverage");
    const parser = result.filter((script) => script.url.endsWith("/markdown.tsx"));
    expect(parser).toHaveLength(1);
    const blocks = parser.flatMap((script) => script.functions.flatMap((fn) => fn.ranges));
    const executed = blocks.reduce((sum, range) => sum + range.count, 0);
    expect(executed).toBeGreaterThan(0);
    return executed + native;
  } finally {
    for (const spy of spies) spy.mockRestore();
    await session.post("Profiler.stopPreciseCoverage");
    session.disconnect();
  }
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

it("keeps parser work linear as hostile inputs grow fourfold", async () => {
  const patterns = new Set<RegExp>();
  const cases = [
    ...Object.entries(inlineCases).map(([name, input]) => ({ name, input, parse: inlineSpans })),
    ...Object.entries(blockCases).map(([name, input]) => ({ name, input, parse: mdBlocks })),
    ...["1 1", "1   x", "1a "].map((unit) => ({ name: `quantity ${unit}`, input: (n: number) => unit.repeat(n), parse: (s: string) => numericColumn([s]) })),
    { name: "table cells", input: (n: number) => "| x `a|b` ".repeat(n), parse: tableCells },
  ];
  for (const { name, input, parse } of cases) {
    let previous = 0;
    const counts = [];
    for (const n of [256, 1024, 4096]) {
      const text = input(n);
      const count = await work(() => parse(text), patterns);
      // A quadratic rescan grows 16x here. Allow a small constant overhead
      // around 4x, and cap total deterministic work per input code unit too.
      expect(count, `${name}, n=${n}`).toBeLessThan(200 * text.length + 1000);
      if (previous) expect(count, `${name}, n=${n}`).toBeLessThanOrEqual(previous * 4.5);
      previous = count;
      counts.push(count);
    }
    if (process.env.RA_MD_WORK_REPORT) console.info(name, counts);
  }
  // JS counters cannot see work inside RegExp.exec. Analyse the actual
  // patterns exercised above, not copies of the patterns in the test.
  // scslre detects single-character polynomial/exponential backtracking;
  // it supplements the dynamic counts, rather than proving every regex safe.
  const unique = new Map([...patterns].map((re) => [re.toString(), re]));
  expect(unique.size).toBeGreaterThanOrEqual(10);
  for (const [source, re] of unique) {
    expect(analyse(re).reports, source).toEqual([]);
  }
// This is the runner's deadlock watchdog, not a performance assertion.
}, 30_000);

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
