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

const watching: (() => void)[] = [];
beforeEach(() => vi.resetModules());
afterEach(() => { watching.splice(0).forEach(stop => stop()); document.body.replaceChildren(); vi.restoreAllMocks(); vi.useRealTimers(); vi.unstubAllGlobals(); history.replaceState(null, "", "/"); });

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

async function watch(firstInstall = false) {
  setUp(true, "/assets/index-new.js");
  const handlers: Record<string, (event: any) => void> = {};
  let controller: object | null = firstInstall ? null : {};
  const sw = navigator.serviceWorker;
  Object.defineProperty(sw, "controller", { configurable: true, get: () => controller });
  sw.addEventListener = ((kind: string, fn: (e: any) => void) => { handlers[kind] = fn; }) as typeof sw.addEventListener;
  sw.removeEventListener = vi.fn();
  const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  const reload = vi.spyOn(location, "reload").mockImplementation(() => {});
  const update = await import("./update");
  watching.push(update.watchUpdates()!);
  await vi.waitFor(async () => expect(await check()).toBe("ready"));
  const visibility = (hide: boolean) => { hidden.mockReturnValue(hide); document.dispatchEvent(new Event("visibilitychange")); };
  return { update, reload, visibility, handlers, claim: () => { controller = {}; handlers.controllerchange({}); } };
}
it("never reloads a visible page when the shell or controller changes", async () => {
  const w = await watch();
  w.handlers.message({ data: { type: "shell" } }); w.claim();
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(w.reload).not.toHaveBeenCalled();
});
it("does not reload on first installation or the first controller claim", async () => {
  const w = await watch(true);
  w.claim();
  w.handlers.message({ data: { type: "shell" } });
  await new Promise(resolve => setTimeout(resolve, 0));
  w.visibility(true);
  expect(w.reload).not.toHaveBeenCalled();
});
it.each(["textarea", "pair-code", "task-form", "contenteditable"])("waits while %s has input, then reloads only on the next safe background transition", async kind => {
  const w = await watch();
  const input = document.createElement(kind === "textarea" || kind === "task-form" ? "textarea" : kind === "contenteditable" ? "div" : "input");
  document.body.append(input);
  if (input instanceof HTMLTextAreaElement || input instanceof HTMLInputElement) input.value = kind === "pair-code" ? "1234 5678" : "unsent draft";
  else { input.setAttribute("contenteditable", "true"); input.textContent = "unsent draft"; }
  w.visibility(true);
  expect(w.reload).not.toHaveBeenCalled();
  if (input instanceof HTMLTextAreaElement || input instanceof HTMLInputElement) input.value = "";
  else input.textContent = "";
  expect(w.reload).not.toHaveBeenCalled();
  w.visibility(false);
  expect(w.reload).not.toHaveBeenCalled();
  w.visibility(true);
  expect(w.reload).toHaveBeenCalledOnce();
});
it("protects retained drafts on other routes and reloads hash-routed documents using location.reload", async () => {
  const w = await watch();
  history.replaceState(null, "", "/#/tasks");
  let draft = "a draft on another route";
  watching.push(w.update.guardUpdateInput(() => !!draft));
  w.visibility(true);
  expect(w.reload).not.toHaveBeenCalled();
  draft = "";
  w.visibility(false); w.visibility(true);
  expect(w.reload).toHaveBeenCalledOnce();
  expect(location.hash).toBe("#/tasks");
  w.visibility(false); w.visibility(true);
  expect(w.reload).toHaveBeenCalledOnce();
});
