import { expect, it } from "vitest";
import { inOwnFile } from "./pages";

it("tells a route whose page is in a file of its own from one drawn at once", async () => {
  // The home, a chat and a project are in the first file.
  for (const route of ["", "s/abc", "p/%2Fwork", "projects"]) expect(inOwnFile(route)).toBe(false);
  for (const route of ["settings", "settings/security", "settings/add-device/x", "tasks", "tasks/new", "devices", "m/1", "q/1", "c/1"]) expect(inOwnFile(route)).toBe(true);
});
