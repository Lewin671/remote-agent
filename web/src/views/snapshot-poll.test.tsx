// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { getCoreHost } from "../core/host";
import { DemoRemote } from "../dev/demo";
import type { Remote } from "../remote";
import { useDerived } from "./actions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement;
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  host?.remove();
  vi.restoreAllMocks();
});

function Probe({ remote }: { remote: Remote }) {
  useDerived(remote, "s-run");
  return null;
}

it("watches the session in CoreHost on mount and unwatches on unmount", async () => {
  const remote = DemoRemote.create(new URLSearchParams());
  const core = getCoreHost(remote);
  const watch = vi.spyOn(core, "watchSession");
  const unwatch = vi.spyOn(core, "unwatchSession");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<Probe remote={remote} />));
  expect(watch).toHaveBeenCalledWith("s-run");
  await act(async () => root!.unmount());
  root = undefined;
  expect(unwatch).toHaveBeenCalledWith("s-run");
  remote.stop();
});
