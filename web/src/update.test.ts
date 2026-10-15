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
  Object.defineProperty(navigator, "serviceWorker", { configurable: true, value: { ready: Promise.resolve({ active }), addEventListener: () => undefined } });
  const page = kept && new Response(`<script type="module" crossorigin src="${kept}"></script>`);
  vi.stubGlobal("caches", { open: async () => ({ match: async () => page }) });
}

beforeEach(() => vi.resetModules());
afterEach(() => vi.unstubAllGlobals());

async function check() {
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
  await act(() => update.checkUpdate(true));
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
