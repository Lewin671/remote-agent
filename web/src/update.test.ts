// @vitest-environment happy-dom
// @vitest-environment-options {"settings":{"handleDisabledFileLoadingAsSuccess":true}}
import { afterEach, beforeEach, expect, it, vi } from "vitest";

// The page runs this build; the service worker answers `ok` and keeps `kept`.
function setUp(ok: boolean, kept?: string) {
  document.head.querySelectorAll("script").forEach((el) => el.remove());
  const script = document.createElement("script");
  script.type = "module";
  script.setAttribute("src", "/assets/index-old.js");
  document.head.append(script);
  const active = {
    postMessage: (_: unknown, [port]: MessagePort[]) => port.postMessage({ ok }),
  };
  Object.defineProperty(navigator, "serviceWorker", { configurable: true, value: { register: async () => ({ active }), ready: Promise.resolve({ active }), addEventListener: () => undefined } });
  const page = kept && new Response(`<script type="module" crossorigin src="${kept}"></script>`);
  vi.stubGlobal("caches", { open: async () => ({ match: async () => page }) });
}

beforeEach(() => vi.resetModules());
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

async function check(advance = false) {
  const update = await import("./update");
  let state = "";
  const { createElement } = await import("react");
  const { createRoot } = await import("react-dom/client");
  const { act } = await import("react");
  function Probe() {
    state = update.useUpdate();
    return null;
  }
  const root = createRoot(document.createElement("div"));
  await act(async () => root.render(createElement(Probe)));
  await act(async () => {
    const pending = update.checkUpdate(true);
    if (advance) await vi.advanceTimersByTimeAsync(200_001);
    await pending;
  });
  root.unmount();
  return state;
}

it("offers a kept page that runs another build", async () => {
  setUp(true, "/assets/index-new.js");
  expect(await check()).toBe("ready");
});

it("says this is the newest when the kept page runs this build", async () => {
  setUp(true, "/assets/index-old.js");
  expect(await check()).toBe("latest");
});

it("says the check failed when the newest could not be kept", async () => {
  setUp(false, "/assets/index-old.js");
  expect(await check()).toBe("failed");
});


it.each(["registration", "activation", "reply", "cache"])("times out the whole update check while waiting for %s and allows retry", async (stage) => {
  setUp(true, "/assets/index-old.js");
  vi.useFakeTimers();
  const sw = navigator.serviceWorker;
  if (stage === "registration") sw.register = () => new Promise(() => undefined);
  if (stage === "activation") {
    sw.register = async () => ({ active: null } as ServiceWorkerRegistration);
    Object.defineProperty(sw, "ready", { configurable: true, value: new Promise(() => undefined) });
  }
  if (stage === "reply") sw.register = async () => ({ active: { postMessage: () => undefined } } as unknown as ServiceWorkerRegistration);
  if (stage === "cache") vi.stubGlobal("caches", { open: () => new Promise(() => undefined) });
  expect(await check(true)).toBe("failed");
  expect(vi.getTimerCount()).toBe(0);
  vi.useRealTimers();
  setUp(true, "/assets/index-old.js");
  expect(await check()).toBe("latest");
});

it("reports registration failure immediately and retries registration on the next check", async () => {
  setUp(true, "/assets/index-old.js");
  navigator.serviceWorker.register = vi.fn().mockRejectedValue(new TypeError("registration failed"));
  expect(await check()).toBe("failed");
  expect(navigator.serviceWorker.register).toHaveBeenCalledOnce();
  setUp(true, "/assets/index-old.js");
  expect(await check()).toBe("latest");
});
