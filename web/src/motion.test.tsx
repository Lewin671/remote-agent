// @vitest-environment happy-dom
// What may play an entrance (useFresh), and the stylesheet's motion tokens.
// The animations themselves need a browser; see docs/design-system.md §4.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { useFresh } from "./motion";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Opts = Parameters<typeof useFresh>[2];

/** Renders useFresh over a list that changes step by step; returns the keys
 * it called fresh after each step. */
async function run(steps: { keys: string[]; armed?: boolean }[], opts?: Opts): Promise<string[][]> {
  const host = document.createElement("div");
  const root = createRoot(host);
  const out: string[][] = [];
  function Probe({ keys, armed }: { keys: string[]; armed: boolean }) {
    const fresh = useFresh(keys, armed, opts);
    out.push(keys.filter(fresh));
    return null;
  }
  try {
    for (const s of steps) await act(async () => root.render(<Probe keys={s.keys} armed={s.armed ?? true} />));
  } finally {
    await act(async () => root.unmount());
  }
  return out;
}

it("plays nothing for what a list opens with", async () => {
  expect((await run([{ keys: ["a", "b", "c"] }]))[0]).toEqual([]);
});

it("plays the entrance of a row that arrives at the end", async () => {
  const r = await run([{ keys: ["a", "b"] }, { keys: ["a", "b", "c"] }]);
  expect(r[1]).toEqual(["c"]);
});

it("plays nothing for older rows loaded above", async () => {
  const r = await run([{ keys: ["c", "d"] }, { keys: ["a", "b", "c", "d"] }]);
  expect(r[1]).toEqual([]);
});

it("plays nothing for a burst, which is a load", async () => {
  const many = Array.from({ length: 20 }, (_, i) => "n" + i);
  const r = await run([{ keys: ["a"] }, { keys: ["a", ...many] }]);
  expect(r[1]).toEqual([]);
});

it("plays nothing while what is shown is still incomplete", async () => {
  const r = await run([{ keys: ["a"], armed: false }, { keys: ["a", "b"], armed: false }, { keys: ["a", "b", "c"] }]);
  expect(r[1]).toEqual([]);
  expect(r[2]).toEqual(["c"]);
});

it("keeps a row's entrance on across later renders, so its class is not taken away mid-play", async () => {
  const r = await run([{ keys: ["a"] }, { keys: ["a", "b"] }, { keys: ["a", "b"] }]);
  expect(r[1]).toEqual(["b"]);
  expect(r[2]).toEqual(["b"]);
});

it("gives no entrance to a row that only replaces one that has gone", async () => {
  const r = await run([{ keys: ["a", "r1"] }, { keys: ["a", "t1"] }], { replaces: (k, gone) => k[0] === "t" && gone.some((g) => g[0] === "r") });
  expect(r[1]).toEqual([]);
});

it("counts arrivals anywhere when asked to (a list that sorts)", async () => {
  const r = await run([{ keys: ["b", "c"] }, { keys: ["a", "b", "c"] }], { anywhere: true });
  expect(r[1]).toEqual(["a"]);
});

// The stylesheet is where durations and the curve live (design-system §4 动效).
// Vitest leaves CSS out of modules, so the file itself is read (tests run in
// web/); typed here because the browser build has no Node types.
const fs = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fs)) as { readFileSync(path: string, enc: string): string };
const css = readFileSync("src/styles.css", "utf8");

it("defines the three durations, the exits at 0.8 of them and one curve", () => {
  for (const decl of [
    "--m-fast: 120ms", "--m-base: 200ms", "--m-slow: 280ms",
    "--m-fast-out: 96ms", "--m-base-out: 160ms", "--m-slow-out: 224ms",
    "--ease-out: cubic-bezier(.2, .8, .2, 1)", "--ease-in: ease-in",
  ])
    expect(css).toContain(decl);
});

it("takes distance, scale and size out under reduced motion, and keeps fades to 120ms", () => {
  const block = /@media \(prefers-reduced-motion: reduce\) \{\s*:root \{([^}]*)\}/.exec(css)?.[1] ?? "";
  for (const decl of ["--m-base: 120ms", "--m-slow: 120ms", "--m-size: 0ms", "--m-dy: 0px", "--m-dy-lg: 0px", "--m-slide: 0%", "--m-scale: 1"])
    expect(block).toContain(decl);
});

it("writes no duration or curve of its own in the motion rules", () => {
  // Everything that moves takes its time and curve from the variables.
  const motion = css.slice(css.indexOf("/* ---------- motion"));
  const literal = motion.match(/(?:animation|transition)[^;{}]*?\b\d*\.?\d+m?s\b/g) ?? [];
  expect(literal.filter((x) => !x.includes("var(")).filter((x) => !/animation-duration/.test(x))).toEqual([]);
});
