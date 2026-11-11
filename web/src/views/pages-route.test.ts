import { expect, it } from "vitest";
import { pageToFetch } from "./pages";

it("tells a route whose page is still to be fetched from one drawn at once", async () => {
  // The home, a chat and a project are in the first file.
  for (const route of ["", "s/abc", "p/%2Fwork", "projects"]) expect(pageToFetch(route)).toBe(false);
  for (const route of ["settings", "settings/security", "settings/add-device/x", "tasks", "tasks/new", "devices", "m/1", "q/1", "c/1"]) expect(pageToFetch(route)).toBe(true);
});
