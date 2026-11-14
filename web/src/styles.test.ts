// Class names in styles.css are short and global. A rule for a bare class
// reaches every element that carries the name, so a name that one component
// uses as a state or as an inner part must not also be styled on its own:
// that is how the session row's third line once got the computer card's fill
// and the waiting dot got a status line's height.
import { readFileSync } from "node:fs";
import { expect, it } from "vitest";

const css = readFileSync(new URL("./styles.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

/** Every class that has a rule whose selector is that class alone. */
function bare(): Set<string> {
  const out = new Set<string>();
  for (const m of css.matchAll(/([^{}]+)\{[^{}]*\}/g))
    for (const sel of m[1].split(",")) {
      const one = /^\.([\w-]+)$/.exec(sel.trim());
      if (one) out.add(one[1]);
    }
  return out;
}

it("styles no state or inner-part name on its own", () => {
  const scoped = [
    // StateMark kinds and other states, always written with their component: .mark.wait, .sw.off
    "wait", "run", "idle", "off", "unread", "on", "sel", "open", "warn", "dis", "last", "stale", "offl",
    // inner parts of a session row and of a list row
    "gut", "sm", "row1", "pv", "mc", "s", "v", "n",
  ];
  expect(scoped.filter((x) => bare().has(x))).toEqual([]);
});

// Read the actual CSS dimensions: a test-only copy of the geometry would
// keep passing if the rendered button moved back over the thumbnail.
it("keeps the draft remove target at the corner, inside its cell and before its neighbour", () => {
  function rule(selector: string) {
    return css.split("}").map(block => block.split("{")).find(([selectors]) => selectors.trim() === selector)?.[1] ?? "";
  }
  function px(selector: string, property: string) {
    const value = new RegExp(`(?:^|;)\\s*${property}:\\s*(-?[0-9]+)(?:px)?\\s*;`).exec(rule(selector));
    expect(value, `${selector} ${property}`).not.toBeNull();
    return Number(value![1]);
  }
  const width = px(".draft-thumb", "width"), height = px(".draft-thumb", "height");
  const side = px(".pic.tthumb", "width");
  const hitWidth = px(".draft-thumb > .x", "width"), hitHeight = px(".draft-thumb > .x", "height");
  const x = width - px(".draft-thumb > .x", "right") - hitWidth;
  const y = px(".draft-thumb > .x", "top");
  const top = height - side - px(".draft-thumb .draft-open", "bottom");
  expect([side, hitWidth, hitHeight]).toEqual([56, 44, 44]);
  expect([x + hitWidth / 2, y + hitHeight / 2]).toEqual([side, top]);
  expect(Math.max(0, side - x) * Math.max(0, y + hitHeight - top)).toBeLessThanOrEqual(22 * 22);
  expect(side / 2 < x && top + side / 2 > y + hitHeight).toBe(true);
  expect(x + hitWidth).toBeLessThanOrEqual(width);
  expect(y).toBeGreaterThanOrEqual(0);
  expect(y + hitHeight).toBeLessThanOrEqual(height);
  expect(x + hitWidth).toBeLessThanOrEqual(width + px(".att-tray", "gap"));
  expect(rule(".att-tray")).toContain("overflow-x: auto");
});

// happy-dom does not lay out container queries. Assert the production CSS
// contract here; Playwright checks actual bounds at normal/enlarged root text.
it("stacks machine rings and cards when their container cannot fit large text", () => {
  expect(css).toMatch(/\.ms-rings-wrap, \.ms-cards\s*\{\s*container-type: inline-size;/);
  const adaptive = css.slice(css.indexOf("@container (max-width: 22rem)"));
  expect(adaptive).toMatch(/\.ms-rings\s*\{[^}]*flex-direction: column/);
  expect(adaptive).toMatch(/\.ms-grid\s*\{[^}]*grid-template-columns: 1fr;/);
  expect(css).toMatch(/\.ms-ring b\s*\{[^}]*font-size: 1.0625rem;/);
});
