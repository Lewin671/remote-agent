// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { Markdown } from "../markdown";

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
it("updates the table overflow hint when its content grows and when scrolled to the end", async () => {
  const observed = new Set<Element>();
  let resized: (entries: { target: Element; contentRect: { width: number } }[]) => void = () => {};
  vi.stubGlobal("ResizeObserver", class {
    constructor(callback: typeof resized) { resized = callback; }
    observe(el: Element) { observed.add(el); }
    disconnect() { observed.clear(); }
  });
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(<Markdown text={"| Name | Value |\n| --- | --- |\n| sample | 1 |"} />));
    const viewport = host.querySelector<HTMLElement>(".mdtable")!;
    const table = host.querySelector("table")!;
    await act(async () => resized([{ target: viewport, contentRect: { width: 300 } }, { target: table, contentRect: { width: 200 } }]));
    expect(viewport.classList.contains("more")).toBe(false);
    // Only the content changes width; the viewport keeps its width.
    await act(async () => { if (observed.has(table)) resized([{ target: table, contentRect: { width: 600 } }]); });
    expect(viewport.classList.contains("more")).toBe(true);
    await act(async () => { viewport.scrollLeft = 300; viewport.dispatchEvent(new Event("scroll")); });
    expect(viewport.classList.contains("more")).toBe(false);
  } finally { await act(async () => root.unmount()); host.remove(); vi.unstubAllGlobals(); }
});
