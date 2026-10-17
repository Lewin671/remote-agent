// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { useRemote } from "../ui";
import { useTray } from "./Attach";
import type { Tray } from "../attach";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
afterEach(() => vi.useRealTimers());

it("renews on the deadline and on return from a suspended page, and cancels when unmounted", async () => {
  vi.useFakeTimers();
  vi.setSystemTime(1000);
  const remote = DemoRemote.create(new URLSearchParams());
  remote.conn = "online";
  remote.prepareImage = async () => ({ bytes: new Uint8Array(1), format: "png", width: 1, height: 1 });
  let count = 0;
  remote.uploadImage = async () => ({ imageId: "id-" + ++count, expiresMs: Date.now() + 100 });
  let tray!: Tray;
  function Draft() {
    useRemote(remote);
    tray = useTray(remote, "s", "mac", 6);
    return <span>{tray.ready() ? "ready" : "waiting"}</span>;
  }
  const host = document.createElement("div");
  const root = createRoot(host);
  try {
    await act(async () => { root.render(<Draft />); });
    await act(async () => { tray.add([new Blob(["source"])]); });
    expect(count).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });
    expect(count).toBe(2);
    expect(tray.ready()).toBe(true);
    // A sleeping browser skipped the timer: foregrounding checks the current time.
    vi.setSystemTime(1300);
    await act(async () => { window.dispatchEvent(new Event("focus")); });
    expect(count).toBe(3);
    await act(async () => { root.unmount(); });
    await vi.advanceTimersByTimeAsync(1000);
    expect(count).toBe(3);
  } finally { host.remove(); }
});
