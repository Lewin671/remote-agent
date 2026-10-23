// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { TerminalHistory } from "../terminal-history";
import * as sgr from "../sgr";
import { TerminalReader } from "./TerminalHistory";
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement;
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined; host?.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals();
});
it("bounds mounted rows, reuses parsed pages, and anchors prepends without losing horizontal width", async () => {
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(100);
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(20);
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(function (this: HTMLElement) {
    return 32 + Number.parseFloat(this.querySelector<HTMLElement>(".thlines")?.style.height || "0");
  });
  const parse = vi.spyOn(sgr, "parseSgrLine");
  const history = new TerminalHistory(() => {});
  history.phase = "reading";
  history.cursor = "earlier";
  history.pages = [{ id: "page", lines: Array.from({ length: 240 }, (_, i) => `\x1b[31mrow ${i} 汉字` + (i === 0 ? "x".repeat(100) : "")) }];
  const load = vi.fn(), leave = vi.fn();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const render = () => root!.render(<TerminalReader history={history} load={load} online font={14} height={20} offset={40} left={120} grid={800} onLeave={leave} />);
  await act(async () => render());
  const box = host.querySelector<HTMLElement>(".terminal-history")!;
  expect(box.scrollLeft).toBe(120);
  expect(host.querySelectorAll(".thline").length).toBeLessThan(30);
  expect(host.textContent).toContain("row 239");
  expect(host.querySelector(".wide")?.textContent).toBe("汉");
  const width = host.querySelector<HTMLElement>(".thlines")!.style.width;
  expect(width).toContain("110ch");
  const parsed = parse.mock.calls.length;
  await act(async () => render());
  expect(parse).toHaveBeenCalledTimes(parsed);
  await act(async () => { box.scrollTop = 500; box.dispatchEvent(new Event("scroll")); });
  const anchor = host.querySelector(".thline")!.textContent;
  history.pages = [{ id: "earlier", lines: ["older 1", "older 2", "older 3"] }, ...history.pages];
  await act(async () => render());
  expect(box.scrollTop).toBe(560);
  expect(host.querySelector(".thline")!.textContent).toBe(anchor);
  expect(box.scrollLeft).toBe(120);
  expect(parse).toHaveBeenCalledTimes(parsed + 3);
  expect(host.querySelector<HTMLElement>(".thlines")!.style.width).toBe(width);
  await act(async () => { box.scrollTop = 0; box.dispatchEvent(new Event("scroll")); });
  expect(load).toHaveBeenCalled();
  await act(async () => {
    box.scrollTop = box.scrollHeight - box.clientHeight;
    box.dispatchEvent(new Event("scroll"));
    box.dispatchEvent(new WheelEvent("wheel", { deltaY: 10, bubbles: true }));
  });
  expect(leave).toHaveBeenCalledTimes(1);
});
