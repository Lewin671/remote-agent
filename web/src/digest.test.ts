import { expect, it } from "vitest";
import { inputDigest } from "./model";

// Vectors written by Go (internal/agent/digest_test.go); tests run in web/.
const fs = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fs)) as { readFileSync(path: string, enc: string): string };
const vectors = JSON.parse(readFileSync("../internal/agent/testdata/digest-vectors.json", "utf8")) as { tool: string; input: string; digest: string }[];

it("computes the daemon's input digest", () => {
  expect(vectors.length).toBeGreaterThan(5);
  for (const v of vectors) expect([v.tool, v.input, inputDigest(v.tool, v.input)]).toEqual([v.tool, v.input, v.digest]);
});
