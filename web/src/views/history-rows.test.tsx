// @vitest-environment happy-dom
import { act, createRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { VirtuosoMockContext } from "react-virtuoso";
import type { Remote } from "../remote";
import { HistoryRows } from "./HistoryRows";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let host: HTMLElement;
afterEach(() => { act(() => root?.unmount()); host?.remove(); root = undefined; });

it("mounts a bounded window of a long history, including the pending footer", async () => {
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const scroller = createRef<HTMLDivElement>();
  const remote = { scroll: new Map() } as unknown as Remote;
  const items = Array.from({ length: 10000 }, (_, i) => ({ key: String(i), text: "message " + (i + 1) }));
  const page = (rows: typeof items) => <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 60 }}>
    <div ref={(el) => {
      scroller.current = el;
      if (!el) return;
      Object.defineProperties(el, {
        offsetHeight: { configurable: true, get: () => 600 },
        scrollHeight: { configurable: true, get: () => Number.parseFloat((el.querySelector(".virtual-history") as HTMLElement)?.style.height || "600") },
      });
      el.getBoundingClientRect = () => new DOMRect(0, 0, 600, 600);
      el.scrollTo = (options) => { if (typeof options === "object") el.scrollTop = options.top ?? 0; el.dispatchEvent(new Event("scroll")); };
    }}><HistoryRows items={rows} render={(it) => <p>{it.text}</p>} scroller={scroller} remote={remote} sid="s" follow={() => true} loaded header={<p>start</p>} footer={<p>pending message</p>} /></div>
  </VirtuosoMockContext.Provider>;
  await act(async () => root!.render(page(items)));
  await vi.waitFor(async () => { await act(async () => { await new Promise((r) => setTimeout(r, 20)); }); expect(host.textContent).toContain("message 10000"); });
  expect(host.querySelectorAll('[data-item-index]').length).toBeLessThan(40);
  expect(host.textContent).toContain("pending message");
  expect(host.querySelectorAll("p").length).toBeLessThan(40);
  await act(async () => root!.render(page([...items, { key: "10000", text: "message 10001" }])));
  await vi.waitFor(async () => { await act(async () => { await new Promise((r) => setTimeout(r, 20)); }); expect(host.textContent).toContain("message 10001"); });
  expect(host.querySelectorAll('[data-item-index]').length).toBeLessThan(40);
});

it("keeps short history and empty history headers and footers visible", async () => {
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const scroller = createRef<HTMLDivElement>(); const remote = { scroll: new Map() } as unknown as Remote;
  const page = (items: {key:string}[]) => <div ref={scroller}><HistoryRows items={items} render={(it) => <p>{it.key}</p>} scroller={scroller} remote={remote} sid="s" follow={() => true} loaded header={<p>start</p>} footer={<p>pending</p>} /></div>;
  await act(async () => root!.render(page([])));
  expect(host.textContent).toBe("startpending");
  await act(async () => root!.render(page([{ key: "hello" }])));
  expect(host.textContent).toBe("starthellopending");
});

it("preserves the reading row when a small history crosses the virtual threshold", async () => {
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const scroller = createRef<HTMLDivElement>(); const remote = { scroll: new Map() } as unknown as Remote;
  let following = true;
  const rows = Array.from({ length: 100 }, (_, i) => ({ key: String(i), text: "row " + (i + 1) }));
  const page = (items: typeof rows) => <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 60 }}><div ref={(el) => {
    scroller.current = el; if (!el) return;
    Object.defineProperties(el, {
      offsetHeight: { configurable: true, get: () => 600 },
      scrollHeight: { configurable: true, get: () => Number.parseFloat((el.querySelector(".virtual-history") as HTMLElement)?.style.height || "6120") || 6120 },
    });
    el.getBoundingClientRect = () => new DOMRect(0, 0, 600, 600);
    el.scrollTo = (options) => { if (typeof options === "object") el.scrollTop = options.top ?? 0; el.dispatchEvent(new Event("scroll")); };
  }}><HistoryRows items={items} render={(it) => <p>{it.text}</p>} scroller={scroller} remote={remote} sid="s" follow={() => following} loaded header={<p>start</p>} footer={<p>pending</p>} /></div></VirtuosoMockContext.Provider>;
  await act(async () => root!.render(page(rows)));
  scroller.current!.scrollTop = 1230;
  following = false;
  remote.scroll.set("s", { top: 1230, stick: false });
  for (const el of host.querySelectorAll<HTMLElement>("[data-history-index]")) {
    const index = Number(el.dataset.historyIndex);
    el.getBoundingClientRect = () => el.classList.contains("history-row")
      ? new DOMRect(0, index * 60 - scroller.current!.scrollTop, 600, 60)
      : new DOMRect(0, -scroller.current!.scrollTop, 600, scroller.current!.scrollHeight);
  }
  await act(async () => root!.render(page([...rows, { key: "100", text: "row 101" }])));
  await vi.waitFor(async () => { await act(async () => { await new Promise((r) => setTimeout(r, 20)); }); expect(host.textContent).toContain("row 20"); });
  expect(host.textContent).not.toContain("row 101");
  expect(scroller.current!.scrollTop).toBe(1230);
});

it("waits for full history before restoring a reclaimed reading position", async () => {
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const scroller = createRef<HTMLDivElement>();
  const remote = { scroll: new Map([["s", { top: 1200, stick: false }]]) } as unknown as Remote;
  const page = (n: number, loaded: boolean) => <div ref={scroller}><HistoryRows items={Array.from({ length: n }, (_, i) => ({ key: String(i) }))} render={(it) => <p>{it.key}</p>} scroller={scroller} remote={remote} sid="s" follow={() => false} loaded={loaded} header={<p>start</p>} footer={<p>pending</p>} /></div>;
  await act(async () => root!.render(page(0, false)));
  expect(host.textContent).toBe("正在恢复阅读位置…");
  await act(async () => root!.render(page(500, false)));
  expect(host.textContent).toBe("正在恢复阅读位置…");
  expect(host.querySelector(".virtual-history")).toBeNull();
  await act(async () => root!.render(page(5000, true)));
  expect(host.querySelector(".virtual-history")).toBeTruthy();
});

it("restores a short but tall history after its cold load finishes", async () => {
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const scroller = createRef<HTMLDivElement>();
  const remote = { scroll: new Map([["s", { top: 1200, stick: false }]]) } as unknown as Remote;
  const page = (n: number, loaded: boolean) => <div ref={scroller}><HistoryRows items={Array.from({ length: n }, (_, i) => ({ key: String(i) }))} render={(it) => <p>{it.key}</p>} scroller={scroller} remote={remote} sid="s" follow={() => false} loaded={loaded} header={<p>start</p>} footer={<p>pending</p>} /></div>;
  await act(async () => root!.render(page(0, false)));
  remote.scroll.set("s", { top: 0, stick: true }); // Empty loading DOM must not erase the original target.
  await act(async () => root!.render(page(20, true)));
  expect(scroller.current!.scrollTop).toBe(1200);
});

it("reveals the selected run after cold loading without restoring the previous reading offset", async () => {
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const scroller = createRef<HTMLDivElement>();
  const remote = { scroll: new Map([["s", { top: 1200, stick: false }]]) } as unknown as Remote;
  const selected: string[] = [];
  const page = (loaded: boolean) => <div ref={scroller}><HistoryRows items={loaded ? ["1", "2", "3"].map(key => ({ key })) : []} focusKey="2" loaded={loaded} remote={remote} sid="s" scroller={scroller} follow={() => false} header={null} footer={null} render={it => <p ref={el => { if (el?.parentElement) el.parentElement.scrollIntoView = () => { selected.push(it.key); scroller.current!.scrollTop = 222; }; }}>{it.key}</p>} /></div>;
  await act(async () => root!.render(page(false)));
  await act(async () => root!.render(page(true)));
  expect(selected).toEqual(["2"]);
  expect(scroller.current!.scrollTop).toBe(222);
  await act(async () => root!.render(null));
  selected.length = 0;
  await act(async () => root!.render(page(true)));
  expect(selected).toEqual(["2"]);
  expect(scroller.current!.scrollTop).toBe(222);
});
it("initially reveals the selected row within a virtual history instead of the old offset", async () => {
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const scroller = createRef<HTMLDivElement>();
  const remote = { scroll: new Map([["s", { top: 1200, stick: false }]]) } as unknown as Remote;
  const items = Array.from({ length: 10000 }, (_, i) => ({ key: String(i), text: "selected row " + i }));
  await act(async () => root!.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 60 }}><div ref={el => {
    scroller.current = el; if (!el) return;
    Object.defineProperties(el, { offsetHeight: { configurable: true, get: () => 600 }, scrollHeight: { configurable: true, get: () => Number.parseFloat((el.querySelector(".virtual-history") as HTMLElement)?.style.height || "600") } });
    el.getBoundingClientRect = () => new DOMRect(0, 0, 600, 600);
    el.scrollTo = options => { if (typeof options === "object") el.scrollTop = options.top ?? 0; el.dispatchEvent(new Event("scroll")); };
  }}><HistoryRows items={items} render={it => <p>{it.text}</p>} scroller={scroller} remote={remote} sid="s" follow={() => false} loaded focusKey="5432" header={null} footer={null} /></div></VirtuosoMockContext.Provider>));
  await vi.waitFor(async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 20)); }); expect(host.textContent).toContain("selected row 5432"); });
  expect(host.querySelectorAll('[data-item-index]').length).toBeLessThan(40);
});
