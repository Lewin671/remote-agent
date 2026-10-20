// @vitest-environment happy-dom
// The terminal's bar of special keys is for touch: a wide window with a
// mouse or trackpad has a keyboard of its own (design-system §6 终端).

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { TerminalView } from "./Terminal";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLElement;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  vi.unstubAllGlobals();
});

function pointer(fine: boolean) {
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: q === "(pointer: fine)" ? fine : false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }));
}

function show(wide: boolean) {
  const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
  r.start();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<TerminalView remote={r} session={r.sessions.get("s-run")!} wide={wide} running />));
  return !!host.querySelector(".keys");
}

it("leaves the key bar out on a wide window with a fine pointer", () => {
  pointer(true);
  expect(show(true)).toBe(false);
});

it("keeps it on touch, and on a narrow window", () => {
  pointer(false);
  expect(show(true)).toBe(true);
  act(() => root?.unmount());
  host.remove();
  pointer(true);
  expect(show(false)).toBe(true);
});
