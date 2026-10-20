// @vitest-environment happy-dom
// What a touch screen gets and a mouse and keyboard do not: the terminal's
// bar of special keys, which a wide window with a mouse or trackpad does
// not need, and shortcuts written in input placeholders.

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

it("writes the send shortcut in placeholders by platform, and none on touch", async () => {
  const { sendKeys } = await import("../ui");
  expect(sendKeys(true, "MacIntel")).toBe("⌘↵");
  expect(sendKeys(true, "iPad")).toBe("⌘↵");
  expect(sendKeys(true, "Win32")).toBe("Ctrl+Enter");
  expect(sendKeys(true, "Linux x86_64")).toBe("Ctrl+Enter");
  expect(sendKeys(false, "MacIntel")).toBe("");
});

it("leaves the shortcut out of the message box on touch", async () => {
  const { SessionPage } = await import("./Session");
  const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
  r.start();
  const box = () => host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!.placeholder;
  for (const [fine, want] of [[false, "消息"], [true, /^消息 · (⌘↵|Ctrl\+Enter) 发送$/]] as const) {
    pointer(fine);
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() => root!.render(<SessionPage remote={r} session={r.sessions.get("s-idle")!} onBack={() => undefined} wide={false} />));
    if (typeof want === "string") expect(box()).toBe(want);
    else expect(box()).toMatch(want);
    act(() => root!.unmount());
    host.remove();
  }
  root = undefined;
});
