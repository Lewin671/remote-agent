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
});

const open = (cls = "") =>
  (document.body.innerHTML = `<div class="dockwrap" style="--content-max: 60px"><div class="dock pending open ${cls}"><button class="pending-sum">…</button><div class="pending-content"><div class="pending-scroll">…</div><div class="pending-actions"><textarea class="field reason"></textarea><div class="btns"><button class="btn no">发送理由并拒绝</button></div></div></div></div></div>`);

it("scrolls an open dock's content, never its actions, within the room it has", () => {
  open();
  const content = getComputedStyle(document.querySelector(".pending-content")!);
  // 240 at most, and never more than the room Dock measured.
  expect(content.maxHeight).toBe("min(240px, 60px)");
  const scroll = getComputedStyle(document.querySelector(".pending-scroll")!);
  expect(scroll.overflowY).toBe("auto");
  expect(scroll.flexShrink).toBe("1");
  expect(getComputedStyle(document.querySelector(".pending-actions")!).flexShrink).toBe("0");
});

it("does not cap a dock whose reason is being written, only the content above it", () => {
  open("reasoning");
  expect(getComputedStyle(document.querySelector(".pending-content")!).maxHeight).toBe("none");
  expect(getComputedStyle(document.querySelector(".pending-scroll")!).maxHeight).toBe("60px");
  const field = getComputedStyle(document.querySelector(".field.reason")!);
  expect(field.minHeight).toBe("44px");
  expect(field.overflowY).toBe("auto");
});

it("scrolls a question page's content, not its buttons", () => {
  document.body.innerHTML = '<div class="page"><div class="question-full">…</div><div class="dock"><div class="btns"><button class="btn pri">提交</button></div></div></div>';
  expect(getComputedStyle(document.querySelector(".page")!).display).toBe("flex");
  const content = getComputedStyle(document.querySelector(".question-full")!);
  expect(content.overflowY).toBe("auto");
  expect(content.minHeight).toBe("0");
  expect(getComputedStyle(document.querySelector(".page > .dock")!).flexShrink).toBe("0");
});

it("keeps the tray's thumbnails square on a wide page", () => {
  document.body.innerHTML = `<div class="desk"><div class="compose-card"><div class="att-tray"><span class="pic tthumb"></span></div></div><span class="pics"><span class="pic local"></span></span></div>`;
  const thumb = getComputedStyle(document.querySelector(".tthumb")!);
  expect([thumb.width, thumb.height]).toEqual(["56px", "56px"]);
  // A sent message's previews keep 3:4 at the wide page's picture height.
  const local = getComputedStyle(document.querySelector(".local")!);
  expect([local.width, local.height]).toEqual(["105px", "140px"]);
});
