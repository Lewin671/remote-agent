// @vitest-environment happy-dom
// The pages fetched on demand: a page whose file the server no longer has
// (a newer build replaced it) says so instead of leaving a blank screen.

import { act, Suspense } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import type { Remote } from "../remote";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock("./Settings", () => { throw new Error("Failed to fetch dynamically imported module"); });

let root: Root | undefined;
let host: HTMLElement;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  sessionStorage.clear();
});

it("shows a way to reload when a page's file is gone and the page has just started over", async () => {
  // It started over a moment ago: not again.
  sessionStorage.setItem("ra-reloaded", String(Date.now()));
  const { SettingsPage } = await import("./pages");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<Suspense fallback={<p>加载中…</p>}><SettingsPage remote={{} as Remote} go={() => undefined} /></Suspense>));
  await vi.waitFor(() => expect(host.textContent).toContain("页面已更新"));
  expect(host.querySelector("button")?.textContent).toBe("重新加载");
});
