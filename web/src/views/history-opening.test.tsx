// @vitest-environment happy-dom
import { act, createRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { HistoryOpening } from "./HistoryOpening";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let host: HTMLDivElement;
afterEach(() => { act(() => root?.unmount()); host?.remove(); root = undefined; vi.useRealTimers(); });
function setup() {
  vi.useFakeTimers();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const scroller = createRef<HTMLDivElement>();
  const retry = vi.fn();
  const draw = (waiting: boolean, error?: string, connected = true) => act(() => root!.render(<>
    <div ref={scroller} />
    <HistoryOpening waiting={waiting} error={error} connected={connected} scroller={scroller} onRetry={retry} />
  </>));
  const advance = (ms: number) => act(() => vi.advanceTimersByTime(ms));
  return { draw, advance, retry, scroller };
}
it("delays message placeholders for 300ms and cancels them when history arrives", () => {
  const t = setup(); t.draw(true); t.advance(299);
  expect(host.querySelector('.history-opening')).toBeNull();
  t.draw(false); t.advance(1000);
  expect(host.querySelector('.history-opening')).toBeNull();
  t.draw(true); t.advance(299);
  t.advance(1);
  expect(host.querySelector('.history-placeholder .me')).not.toBeNull();
  expect(host.querySelector('.history-placeholder .ai')).not.toBeNull();
  t.draw(false); t.advance(6000);
  expect(host.querySelector('.history-opening')).toBeNull();
});
it("leaves offline status to the connection bar", () => {
  const t = setup(); t.draw(true, undefined, false); t.advance(6000);
  expect(host.querySelector('.history-placeholder')).not.toBeNull();
  expect(host.querySelector('[role=status]')).toBeNull();
});
it("replaces placeholders after five seconds with an inline retry and keeps errors immediate", () => {
  const t = setup(); t.draw(true); t.advance(5000);
  expect(host.querySelector('.history-placeholder')).toBeNull();
  expect(host.querySelector('[role=status]')?.textContent).toContain('已输入的内容会保留');
  act(() => host.querySelector<HTMLButtonElement>('button')!.click());
  expect(t.retry).toHaveBeenCalledOnce();
  t.draw(false); t.draw(true, 'synthetic failure');
  expect(host.querySelector('[role=status]')?.textContent).toContain('synthetic failure');
});
it("covers virtual-list settling without entering the scroller or changing its position", async () => {
  const t = setup(); t.draw(false); t.scroller.current!.scrollTop = 321;
  await act(async () => {
    t.scroller.current!.classList.add('mo-hold');
    await vi.advanceTimersByTimeAsync(0);
  });
  t.advance(300);
  expect(host.querySelector('.history-placeholder')).not.toBeNull();
  expect(t.scroller.current!.children).toHaveLength(0);
  expect(t.scroller.current!.scrollTop).toBe(321);
  await act(async () => {
    t.scroller.current!.classList.remove('mo-hold');
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(host.querySelector('.history-opening')).toBeNull();
});
