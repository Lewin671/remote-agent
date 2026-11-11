// @vitest-environment happy-dom
// The message box that grows with its text: one measure of the text per
// change, and an ease only when the height changes.

import { act, useRef, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { useAutoGrow } from "./Composer";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLElement;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

it("measures the text once per change and eases only when the height changes", async () => {
  vi.stubEnv("MODE", "production");
  const animate = vi.fn(() => ({ addEventListener() {}, cancel() {}, id: "" }));
  Object.defineProperty(Element.prototype, "animate", { configurable: true, value: animate });
  Object.defineProperty(Element.prototype, "getAnimations", { configurable: true, value: () => [] });
  // The text needs 40px for one line, 80 for two; a read of the height is counted.
  let reads = 0;
  let rects = 0;
  let needed = 40;
  let set!: (text: string) => void;
  function Box() {
    const ref = useRef<HTMLTextAreaElement>(null);
    const [text, setText] = useState("");
    set = setText;
    useAutoGrow(ref, text);
    return <textarea ref={ref} value={text} onChange={() => undefined} />;
  }
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<Box />));
  const area = host.querySelector("textarea")!;
  Object.defineProperty(area, "scrollHeight", { configurable: true, get: () => { reads++; return needed; } });
  Object.defineProperty(area, "offsetHeight", { configurable: true, get: () => 40 });
  Object.defineProperty(area, "getBoundingClientRect", { configurable: true, value: () => { rects++; return { height: parseFloat(area.style.height) || 40 }; } });
  // The first fit is where it starts.
  await act(async () => set("a"));
  const base = reads;
  animate.mockClear();
  for (const text of ["ab", "abc", "abcd"]) await act(async () => set(text));
  expect(reads - base).toBe(3);
  // Nothing else asks the page for a size.
  expect(rects).toBe(0);
  expect(animate).not.toHaveBeenCalled();
  needed = 80;
  await act(async () => set("abcd\ne"));
  expect(reads - base).toBe(4);
  expect(animate).toHaveBeenCalledTimes(1);
  expect(rects).toBe(0);
  expect(area.style.height).toBe("80px");
});
