// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App";
import { Remote } from "./remote";
import { DemoRemote } from "./dev/demo";
import { forgetAccount } from "./storage";
import * as securityClient from "./core/security-client";
import { SecurityBootstrapKind, SecurityWorkflowStatusSchema } from "./gen/remoteagent/core/v1/security_pb";

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
  vi.spyOn(securityClient, "createStandaloneSecurityClient").mockReturnValue({
    client: {
      bootstrap: vi.fn(async () => create(SecurityWorkflowStatusSchema, { bootstrap: SecurityBootstrapKind.READY, server: "https://ra.example.com", deviceId: "me", accountId: "acc", isMember: true })),
    } as unknown as securityClient.SecurityClient,
    dispose: vi.fn(),
  });
  // An IndexedDB delete that completes asynchronously, as in the browser.
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
  const reload = vi
    .spyOn(location, "reload")
    .mockImplementation(() => undefined);
  const root = createRoot(document.createElement("div"));
  try {
    await act(async () => root.render(<App />));
    await vi.waitFor(() => expect(start).toHaveBeenCalled());
    location.hash = "/settings";
    // The source page leaves reloading to its caller, after cache cleanup.
    await act(async () => forgetAccount());
    expect(stop).not.toHaveBeenCalled();
    expect(reload).not.toHaveBeenCalled();
    // A separate module instance models the other tab's own channel.
    vi.resetModules();
    const other = await import("./storage");
    await act(async () => other.forgetAccount());
    expect(stop).toHaveBeenCalled();
    expect(location.hash).toBe("");
    expect(reload).toHaveBeenCalledOnce();
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
    await expect(forgetAccount()).rejects.toBeDefined();
  });
}
