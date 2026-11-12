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
  (document.body.innerHTML = `<div class="dockwrap" style="--content-max: 60px"><div class="dock pending open ${cls}"><div class="tc"><div class="k pending-head"><button class="pending-sum">…</button></div><div class="pending-content"><div class="pending-scroll">…</div><div class="pending-actions"><textarea class="inp"></textarea><div class="btns"><button class="btn">取消</button><button class="btn pri">不允许</button></div></div></div></div></div></div>`);

it("scrolls an open dock's content, never its actions, within the room it has", () => {
  open();
  const content = getComputedStyle(document.querySelector(".pending-content")!);
  // What Dock measured: a share of the chat column within the room it has.
  expect(content.maxHeight).toBe("60px");
  const scroll = getComputedStyle(document.querySelector(".pending-scroll")!);
  expect(scroll.overflowY).toBe("auto");
  expect(scroll.flexShrink).toBe("1");
  expect(getComputedStyle(document.querySelector(".pending-actions")!).flexShrink).toBe("0");
});

it("does not cap a dock whose reason is being written, only the content above it", () => {
  open("reasoning");
  expect(getComputedStyle(document.querySelector(".pending-content")!).maxHeight).toBe("none");
  expect(getComputedStyle(document.querySelector(".pending-scroll")!).maxHeight).toBe("60px");
  const field = getComputedStyle(document.querySelector(".tc .inp")!);
  expect(field.minHeight).toBe("48px");
  expect(field.maxHeight).toBe("140px");
});

it("scrolls a waiting item's page content, not its buttons, as high as the page allows", () => {
  document.body.innerHTML = '<div class="page dpage"><div class="tc"><div class="k">…</div><div class="pending-content"><div class="pending-scroll">…</div><div class="pending-actions"><div class="btns"><button class="btn pri">提交</button></div></div></div></div></div>';
  expect(getComputedStyle(document.querySelector(".page")!).display).toBe("flex");
  const card = getComputedStyle(document.querySelector(".dpage > .tc")!);
  expect(card.minHeight).toBe("0");
  expect(card.flexShrink).toBe("1");
  expect(getComputedStyle(document.querySelector(".pending-content")!).maxHeight).toBe("none");
  const content = getComputedStyle(document.querySelector(".pending-scroll")!);
  expect(content.overflowY).toBe("auto");
  expect(content.minHeight).toBe("0");
  expect(getComputedStyle(document.querySelector(".pending-actions")!).flexShrink).toBe("0");
});

it("keeps the tray's thumbnails square on a wide page", () => {
  document.body.innerHTML = `<div class="desk"><div class="cbar"><div class="att-tray"><span class="pic tthumb"></span></div></div><span class="pics"><span class="pic local"></span></span></div>`;
  const thumb = getComputedStyle(document.querySelector(".tthumb")!);
  expect([thumb.width, thumb.height]).toEqual(["56px", "56px"]);
  // Message preview geometry is now supplied from each image, not fixed in CSS.
});

it("keeps an approval's or a plan's buttons to 480 on a wide window", () => {
  document.body.innerHTML = `<div class="dock"><div class="btns decide"><button class="btn no">不允许</button><button class="btn pri">允许</button></div></div>`;
  expect(innerWidth).toBeGreaterThanOrEqual(900);
  expect(getComputedStyle(document.querySelector(".btns.decide")!).maxWidth).toBe("480px");
});

it("cuts a dark card's command to whole lines with no scroll of its own, and none once opened", () => {
  // happy-dom drops line-clamp, so the rules are read as written.
  const css = readFileSync("src/styles.css", "utf8");
  const rule = (sel: string) => css.split("\n").find((l) => l.startsWith(sel + " {")) ?? "";
  const cut = rule(".tc .cmd.clamp");
  expect(cut).toContain("display: -webkit-box;");
  expect(cut).toContain("-webkit-line-clamp: var(--lines, 5);");
  expect(cut).toContain("overflow: hidden;");
  // The block itself wraps anywhere rather than widening the card.
  const whole = rule(".tc .cmd");
  expect(whole).toContain("white-space: pre-wrap;");
  expect(whole).toContain("overflow-wrap: anywhere;");
  expect(whole).not.toContain("overflow-y");
});

it("draws glass opaque when the system asks for less transparency", () => {
  const css = readFileSync("src/styles.css", "utf8");
  const reduced = css.split("\n").find((l) => l.startsWith("@media (prefers-reduced-transparency: reduce) { .glass {")) ?? "";
  expect(reduced).toContain("background: var(--gbg);");
  expect(reduced).toContain("backdrop-filter: none;");
});

it("keeps the running ring still when the system asks for less motion", () => {
  const css = readFileSync("src/styles.css", "utf8");
  expect(css).toContain("@media (prefers-reduced-motion: reduce) { .mark.run { animation: none; } }");
  expect(css).toContain("@media (prefers-reduced-motion: reduce) { .ic.spin { animation: none; } }");
});
