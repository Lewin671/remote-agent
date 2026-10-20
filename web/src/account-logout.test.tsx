// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App";
import { Remote } from "./remote";
import { DemoRemote } from "./dev/demo";
import { forgetAccount } from "./storage";

vi.mock("./eventcache", () => ({
  clearEventCache: vi.fn(async () => undefined),
}));
vi.mock("./views/Pairing", () => ({ Setup: () => <p>加入账户</p> }));
(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

class Channel {
  static all = new Set<Channel>();
  onmessage?: (event: MessageEvent) => void;
  constructor(readonly name: string) {
    Channel.all.add(this);
  }
  postMessage(data: unknown) {
    for (const c of Channel.all)
      if (c !== this && c.name === this.name) {
        const e = new MessageEvent("message", { data });
        c.onmessage?.(e);
      }
  }
  close() {
    Channel.all.delete(this);
  }
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  Channel.all.clear();
});

it("stops another tab and reloads the joining page after the user forgets the account", async () => {
  vi.stubGlobal("BroadcastChannel", Channel);
  vi.spyOn(await import("./storage"), "loadAccount").mockResolvedValue(
    undefined,
  );
  // An IndexedDB delete that completes asynchronously, as in the browser.
  const deleted = vi.fn();
  vi.stubGlobal("indexedDB", {
    deleteDatabase: () => {
      const req = { onsuccess: () => undefined };
      queueMicrotask(() => req.onsuccess());
      return req;
    },
    open: () => {
      const tx = {
        oncomplete: () => undefined,
        objectStore: () => ({
          openCursor: () => {
            const req = { result: null, onsuccess: () => undefined };
            queueMicrotask(() => { req.onsuccess(); tx.oncomplete(); });
            return req;
          },
          delete: () => {
            deleted();
            queueMicrotask(() => tx.oncomplete());
            return { result: undefined };
          },
        }),
      };
      const req = {
        result: { transaction: () => tx, close: () => undefined },
        onsuccess: () => undefined,
      };
      queueMicrotask(() => req.onsuccess());
      return req;
    },
  });
  const remote = DemoRemote.create(new URLSearchParams());
  vi.spyOn(await import("./remote"), "resumeRegistration").mockResolvedValue(
    undefined,
  );
  vi.spyOn(Remote, "load").mockResolvedValue(remote);
  vi.spyOn(remote, "start").mockImplementation(() => undefined);
  const stop = vi.spyOn(remote, "stop");
  const reload = vi
    .spyOn(location, "reload")
    .mockImplementation(() => undefined);
  const root = createRoot(document.createElement("div"));
  try {
    await act(async () => root.render(<App />));
    location.hash = "/settings";
    // The source page leaves reloading to its caller, after cache cleanup.
    await act(async () => forgetAccount());
    expect(stop).not.toHaveBeenCalled();
    expect(reload).not.toHaveBeenCalled();
    // A separate module instance models the other tab's own channel.
    vi.resetModules();
    const other = await import("./storage");
    await act(async () => other.forgetAccount());
    expect(deleted).toHaveBeenCalledTimes(4);
    expect(stop).toHaveBeenCalled();
    expect(location.hash).toBe("");
    expect(reload).toHaveBeenCalledOnce();
  } finally {
    act(() => root.unmount());
  }
  expect(Channel.all.size).toBe(0);
});

for (const failure of ["onblocked", "onerror"] as const) {
  it(`keeps the account available when security erasure fails: ${failure}`, async () => {
    const open = vi.fn();
    vi.stubGlobal("indexedDB", {
      open,
      deleteDatabase: () => {
        const req = {
          onblocked: () => undefined,
          onerror: () => undefined,
          error: new Error("erase failed"),
        };
        queueMicrotask(() => req[failure]());
        return req;
      },
    });
    await expect(forgetAccount()).rejects.toBeDefined();
    expect(open).not.toHaveBeenCalled();
  });
}
