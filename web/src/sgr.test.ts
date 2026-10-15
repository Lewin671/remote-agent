// The contract for turning one line of `capture-pane -e` terminal history
// into styled runs, which Android and iOS read from the same file
// (testdata/terminal/history-sgr.json). A change here changes all three.

import { describe, expect, it } from "vitest";
import raw from "../../testdata/terminal/history-sgr.json?raw";
import { parseSgrLine, widthSegments } from "./sgr";

const cases = JSON.parse(raw) as { about: string; cases: { name: string; line: string; runs: unknown[] }[] };

/** The columns a line fills, one per narrow character and two per wide one. */
const cells = (line: string) => widthSegments(line).reduce((n, s) => n + [...s.text].length * (s.wide ? 2 : 1), 0);

describe("shared terminal history cases", () => {
  for (const c of cases.cases) it(c.name, () => expect(parseSgrLine(c.line)).toEqual(c.runs));
});

describe("width segments", () => {
  it("splits narrow and wide stretches", () => {
    expect(widthSegments("中a文b")).toEqual([
      { text: "中", wide: true },
      { text: "a", wide: false },
      { text: "文", wide: true },
      { text: "b", wide: false },
    ]);
  });

  it("keeps a pure ASCII line as one narrow stretch", () => {
    expect(widthSegments("plain text")).toEqual([{ text: "plain text", wide: false }]);
  });

  it("counts fullwidth forms as wide", () => {
    expect(widthSegments("ＡＢ")).toEqual([{ text: "ＡＢ", wide: true }]);
  });

  it("lines a mixed CJK and ASCII line up under its ruler", () => {
    const line = "ab中文cd中文ef中文";
    const ruler = "123456789012345678"; // one mark per column
    expect(cells(line)).toBe(18);
    expect(ruler).toHaveLength(cells(line));
  });
});
