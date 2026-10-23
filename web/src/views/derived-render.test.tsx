// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { Remote } from "../remote";
import * as model from "../model";
import { SessionPage } from "./Session";

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement;
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined; host?.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals();
});
it("does not rebuild the displayed timeline or recorded index on unrelated notifications", async () => {
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  const r = DemoRemote.create(new URLSearchParams());
  vi.spyOn(r, "currentDerived").mockImplementation((sid, d) => Remote.prototype.currentDerived.call(r, sid, d));
  vi.spyOn(r, "refreshLiveSnapshot").mockResolvedValue(undefined);
  const derive = vi.spyOn(model, "derive"), timeline = vi.spyOn(model, "timeline");
  const flatMap = vi.spyOn(r.events("s-run"), "flatMap");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<SessionPage remote={r} session={r.sessions.get("s-run")!} wide onBack={() => {}} />));
  expect(timeline).toHaveBeenCalled();
  const indexBuilds = () => flatMap.mock.calls.filter(([f]) => f.toString().includes("requestId")).length;
  expect(indexBuilds()).toBeGreaterThan(0);
  const counts = [derive.mock.calls.length, timeline.mock.calls.length, indexBuilds()];
  await act(async () => { r.conn = "offline"; r.touch(); });
  await act(async () => { r.devices.clear(); r.touch(); });
  await act(async () => { r.sessions.get("s-plan")!.updatedMs++; r.touch(); });
  expect([derive.mock.calls.length, timeline.mock.calls.length, indexBuilds()]).toEqual(counts);
  r.stop();
});
