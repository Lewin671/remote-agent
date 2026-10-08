// The cases both clients read Markdown by. Android runs the same file
// (MarkdownCasesTest.kt), so a change here changes both or fails one.
//
// To add a case, add its name and md to testdata/markdown/cases.json and run
// `UPDATE_MD_CASES=1 pnpm test markdown.cases`, then read the `want` it wrote.

import { describe, expect, it } from "vitest";
import raw from "../../testdata/markdown/cases.json?raw";
import { boldHeading, inlineSpans, looseList, mdBlocks, numericColumn } from "./markdown";

const cases = JSON.parse(raw) as {
  blocks: { name: string; md: string; want?: unknown }[];
  inline: { name: string; md: string; want?: unknown }[];
  numericColumn: { cells: string[]; want: boolean }[];
  boldHeading: { md: string; want: boolean }[];
  looseList: { items: string[]; want: boolean }[];
};

// JSON has no undefined: a column without alignment is null there.
const plain = (v: unknown) => JSON.parse(JSON.stringify(v));

// The web project is typed for the browser, so Node is reached untyped.
const node = (globalThis as { process?: { env: Record<string, string | undefined> } }).process;
if (node?.env.UPDATE_MD_CASES) {
  for (const c of cases.blocks) c.want = plain(mdBlocks(c.md));
  for (const c of cases.inline) c.want = plain(inlineSpans(c.md));
  const fs = "node:fs";
  const { writeFileSync } = await import(/* @vite-ignore */ fs);
  writeFileSync(new URL("../../testdata/markdown/cases.json", import.meta.url), JSON.stringify(cases, null, 1) + "\n");
}

describe("shared Markdown cases", () => {
  for (const c of cases.blocks) it("blocks: " + c.name, () => expect(plain(mdBlocks(c.md))).toEqual(c.want));
  for (const c of cases.inline) it("inline: " + c.name, () => expect(plain(inlineSpans(c.md))).toEqual(c.want));
  for (const c of cases.numericColumn) it("numeric column: " + c.cells.join(" | "), () => expect(numericColumn(c.cells)).toBe(c.want));
  for (const c of cases.looseList) it("loose list: " + c.items.join(" | ").slice(0, 40), () => expect(looseList(c.items)).toBe(c.want));
  for (const c of cases.boldHeading) it("bold heading: " + JSON.stringify(c.md), () => expect(boldHeading(c.md)).toBe(c.want));
});
