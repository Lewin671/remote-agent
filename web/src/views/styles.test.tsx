// @vitest-environment happy-dom
// Checks on the stylesheet's cascade that layout-free tests can make.

import { beforeAll, expect, it } from "vitest";

// Vitest leaves CSS out of modules, so read the file itself (tests run in
// web/). Typed here:
// the browser build has no Node types.
const fs = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fs)) as { readFileSync(path: string, enc: string): string };

beforeAll(() => {
  const style = document.createElement("style");
  style.textContent = readFileSync("src/styles.css", "utf8");
  document.head.append(style);
  document.body.innerHTML = '<div class="dock reasoning"><textarea class="field reason"></textarea><div class="btns"><button class="btn danger">发送理由并拒绝</button></div></div>';
});

it("lets the reason field, not the button, give way in a capped dock", () => {
  const field = getComputedStyle(document.querySelector(".field.reason")!);
  expect(field.flexShrink).toBe("1");
  expect(field.overflowY).toBe("auto");
  expect(field.minHeight).toBe("44px");
  expect(getComputedStyle(document.querySelector(".dock > .btns")!).flexShrink).toBe("0");
});

it("does not cap a dock that holds only the reason and its button", () => {
  const dock = getComputedStyle(document.querySelector(".dock.reasoning")!);
  expect(dock.maxHeight).toBe("none");
  expect(dock.overflow).toBe("visible");
});

it("never cuts a dock's buttons in a short chat column", () => {
  // Hidden overflow would let the 45% cap cut them; visible overflow keeps
  // the card at least as tall as its controls.
  document.body.innerHTML = '<div class="dock"><div class="dscroll">…</div><div class="btns"><button class="btn">允许</button></div></div>';
  expect(getComputedStyle(document.querySelector(".dock")!).overflow).toBe("visible");
  expect(getComputedStyle(document.querySelector(".dock > .dscroll")!).overflowY).toBe("auto");
});
