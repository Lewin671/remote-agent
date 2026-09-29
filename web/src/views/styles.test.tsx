// @vitest-environment happy-dom
// Checks on the stylesheet's cascade that layout-free tests can make.

import { expect, it } from "vitest";

// Vitest leaves CSS out of modules, so read the file itself (tests run in
// web/). Typed here:
// the browser build has no Node types.
const fs = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fs)) as { readFileSync(path: string, enc: string): string };

it("lets the reason field, not the button, give way in a capped dock", () => {
  const style = document.createElement("style");
  style.textContent = readFileSync("src/styles.css", "utf8");
  document.head.append(style);
  document.body.innerHTML = '<div class="dock reasoning"><textarea class="field reason"></textarea><div class="btns"><button class="btn danger">发送理由并拒绝</button></div></div>';
  const field = getComputedStyle(document.querySelector(".field.reason")!);
  expect(field.flexShrink).toBe("1");
  expect(field.overflowY).toBe("auto");
  expect(field.minHeight).toBe("44px");
  expect(getComputedStyle(document.querySelector(".dock > .btns")!).flexShrink).toBe("0");
});
