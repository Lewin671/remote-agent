// @vitest-environment happy-dom
// The wide redo (design-system §2 宽屏表面, §6 宽屏) in what a layout-free test can see:
// the surfaces .desk maps the tokens to, what a path does when it is cut, the language
// a code block carries, and where focus goes in a dialog.
import { act, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeAll, expect, it } from "vitest";
import { Markdown } from "../markdown";
import { PathText, Sheet } from "../ui";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const fs = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fs)) as { readFileSync(path: string, enc: string): string };

beforeAll(() => {
  const style = document.createElement("style");
  style.textContent = readFileSync("src/styles.css", "utf8");
  document.head.append(style);
});
afterEach(() => { document.body.innerHTML = ""; delete document.documentElement.dataset.theme; });

const bg = (sel: string) => getComputedStyle(document.querySelector(sel)!).backgroundColor;

it.each([
  ["dark", ["#111113", "#19191c", "#222226", "#2b2b30"]],
  ["light", ["#ececef", "#ffffff", "#f4f4f6", "#ffffff"]],
])("draws the wide window in four surfaces, %s", (theme, [s0, s1, s2, s3]) => {
  document.documentElement.dataset.theme = theme;
  document.body.innerHTML = `<div class="desk"><aside class="side"></aside><main class="dm"><div class="grp"></div><div class="me"><div>hi</div></div></main><div class="overlay"><div class="sheet"></div></div></div><div class="phone"><div class="page"></div></div>`;
  expect(bg(".desk")).toBe(s0);
  expect(bg(".dm")).toBe(s1);
  expect(bg(".dm .grp")).toBe(s2);
  expect(bg(".dm .me > div")).toBe(s2);
  expect(bg(".sheet")).toBe(s3);
  // A phone keeps its own page: black in the dark theme, white in the light.
  expect(bg(".page")).toBe(theme === "dark" ? "#000000" : "#ffffff");
});

it("cuts a path in the middle: its start and its last piece stay", () => {
  const out = renderToStaticMarkup(<PathText path="~/Code/ai-bot/remote-agent" />);
  expect(out).toContain('title="~/Code/ai-bot/remote-agent"');
  expect(out).toContain('<span class="pth-head">~/Code/ai-bot/</span>');
  expect(out).toContain('<span class="pth-tail">remote-agent</span>');
  // Nothing to cut in a name with no folder, or a folder that ends in a slash.
  expect(renderToStaticMarkup(<PathText path="foo" />)).toBe("foo");
  expect(renderToStaticMarkup(<PathText path="~/Code/" />)).toBe("~/Code/");
});

it("carries a code block's language for the line above it", () => {
  expect(renderToStaticMarkup(<Markdown text={"```go\nx := 1\n```"} />)).toContain('class="cb" data-lang="go"');
  expect(renderToStaticMarkup(<Markdown text={"```\nx := 1\n```"} />)).not.toContain("data-lang");
});

function Host({ onClose }: { onClose: () => void }) {
  return (
    <>
      <button id="opener">open</button>
      <Sheet onClose={onClose} title="设置" action={{ label: "完成", onClick: onClose }}>
        <button id="first">一</button>
        <button id="last">二</button>
      </Sheet>
    </>
  );
}

it("moves focus into a dialog, keeps Tab inside it and gives focus back when it closes", async () => {
  const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
  const Probe = () => {
    const [open, setOpen] = useState(false);
    const ref = useRef<HTMLButtonElement>(null);
    return <><button id="outer" ref={ref} onClick={() => setOpen(true)}>x</button>{open && <Host onClose={() => setOpen(false)} />}</>;
  };
  await act(async () => root.render(<Probe />));
  const outer = host.querySelector<HTMLElement>("#outer")!;
  outer.focus();
  await act(async () => outer.click());
  const panel = document.querySelector<HTMLElement>(".sheet")!;
  expect(document.activeElement).toBe(panel);
  const tab = (shift = false) => { const e = new KeyboardEvent("keydown", { key: "Tab", shiftKey: shift, bubbles: true, cancelable: true }); (document.activeElement ?? document.body).dispatchEvent(e); return e.defaultPrevented; };
  // From the panel, Shift+Tab goes round to the last control; Tab from the last goes to the first.
  expect(tab(true)).toBe(true);
  expect((document.activeElement as HTMLElement).id).toBe("last");
  expect(tab()).toBe(true);
  expect(document.activeElement).not.toBe(outer);
  expect(panel.contains(document.activeElement)).toBe(true);
  // Closing hands focus back to what had it.
  await act(async () => (document.querySelector("button.side-act") as HTMLElement).click());
  expect(document.activeElement).toBe(outer);
  await act(async () => root.unmount());
});
