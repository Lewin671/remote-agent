// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App";
import { CoreHost } from "./core/host";
import { Remote } from "./remote";
import { DemoRemote } from "./dev/demo";
import { clearAllSecurityV4, securityStoreV4 } from "./core/security-store-v4";
import { mockAppReadyBootstrap } from "./test/security-host-fixture";

vi.mock("./eventcache", () => ({
  clearEventCache: vi.fn(async () => undefined),
}));
vi.mock("./views/Pairing", () => ({ Setup: () => <p>加入账户</p> }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

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

it("stops another tab and resets in-place via CoreHost without calling location.reload", async () => {
  vi.stubGlobal("BroadcastChannel", Channel);
  mockAppReadyBootstrap();
  const externalLogoutSpy = vi.spyOn(CoreHost.prototype, "externalLogout");

  const deleted = vi.fn();
  vi.stubGlobal("indexedDB", {
    deleteDatabase: () => {
      const req = { onsuccess: () => undefined };
      queueMicrotask(() => req.onsuccess());
      return req;
    },
    open: () => {
      let scheduled = false;
      const tx = {
        oncomplete: () => undefined,
        objectStore: () => {
          const finish = () => {
            if (!scheduled) {
              scheduled = true;
              queueMicrotask(() => tx.oncomplete());
            }
          };
          return {
            clear: () => {
              deleted();
              finish();
              return {};
            },
            openCursor: () => {
              const req = { result: null, onsuccess: () => undefined };
              queueMicrotask(() => { req.onsuccess(); finish(); });
              return req;
            },
            delete: () => {
              deleted();
              finish();
              return { result: undefined };
            },
          };
        },
      };
      const req = {
        result: { objectStoreNames: { contains: () => true }, transaction: () => tx, close: () => undefined },
        onsuccess: () => undefined,
      };
      queueMicrotask(() => req.onsuccess());
      return req;
    },
  });
  const remote = DemoRemote.create(new URLSearchParams());
  vi.spyOn(Remote, "load").mockResolvedValue(remote);
  const start = vi.spyOn(remote, "start").mockImplementation(() => undefined);
  const stop = vi.spyOn(remote, "stop");
  const reload = vi.spyOn(location, "reload").mockImplementation(() => undefined);
  const root = createRoot(document.createElement("div"));
  try {
    await act(async () => root.render(<App />));
    await vi.waitFor(() => expect(start).toHaveBeenCalled());
    location.hash = "/settings";
    await act(async () => clearAllSecurityV4(securityStoreV4));
    expect(stop).not.toHaveBeenCalled();
    expect(reload).not.toHaveBeenCalled();

    // A separate module instance models the other tab's own channel.
    vi.resetModules();
    const other = await import("./core/security-store-v4");
    await act(async () => other.clearAllSecurityV4(other.securityStoreV4));
    await vi.waitFor(() => {
      expect(externalLogoutSpy).toHaveBeenCalledOnce();
      expect(stop).toHaveBeenCalled();
      expect(location.hash).toBe("");
    });
    expect(reload).not.toHaveBeenCalled();
  } finally {
    act(() => root.unmount());
  }
  expect(Channel.all.size).toBe(0);
});

for (const failure of ["onabort", "onerror"] as const) {
  it(`keeps the account available when security erasure fails: ${failure}`, async () => {
    vi.stubGlobal("indexedDB", {
      open: () => {
        const tx: Record<string, unknown> = {
          error: new Error("erase failed"),
          objectStore: () => ({ clear: () => ({}) }),
        };
        const req: Record<string, unknown> = {
          result: { transaction: () => tx, close: () => undefined },
          onsuccess: () => undefined,
        };
        queueMicrotask(() => {
          (req.onsuccess as () => void)();
          queueMicrotask(() => (tx[failure] as () => void)?.());
        });
        return req;
      },
    });
    await expect(clearAllSecurityV4(securityStoreV4)).rejects.toBeDefined();
  });
}
