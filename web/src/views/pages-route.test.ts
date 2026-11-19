import { createElement, Suspense } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { inOwnFile, page } from "./pages";

it("tells a route whose page is in a file of its own from one drawn at once", async () => {
  // The home, a chat and a project are in the first file.
  for (const route of ["", "s/abc", "p/%2Fwork", "projects"]) expect(inOwnFile(route)).toBe(false);
  for (const route of ["settings", "settings/notifications", "settings/clients", "settings/licenses", "settings/security", "settings/add-device/x", "tasks", "tasks/new", "devices", "m/1", "m/1/storage", "m/1/direct", "m/1/cli/1", "machine-status/1", "q/1", "c/1"]) expect(inOwnFile(route)).toBe(true);
});

it("renders a preloaded lazy page synchronously without falling back to Suspense", async () => {
  const Sample = page(() => Promise.resolve({ default: () => createElement("span", null, "ready") }));
  await Sample.preload();
  const html = renderToStaticMarkup(createElement(Suspense, { fallback: createElement("span", null, "loading") }, createElement(Sample)));
  expect(html).toContain("ready");
  expect(html).not.toContain("loading");
});
