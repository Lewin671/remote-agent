// The service worker's keeping of the page (public/sw.js), run against a
// made-up cache, network and set of open pages.
import { afterEach, expect, it, vi } from "vitest";
import source from "../public/sw.js?raw";

const PAGE = '<script type="module" src="/assets/index-new.js"></script><link rel="stylesheet" href="/assets/index-new.css">';

type Answer = Response | "stall" | "fail";

function load(answers: Record<string, Answer[]>) {
  const stored = new Map<string, Response>();
  const cache = {
    match: async (key: string) => stored.get(key)?.clone(),
    put: async (key: string, res: Response) => void stored.set(key, res),
    keys: async () => [...stored.keys()].map((k) => ({ url: "https://app.example.com" + k })),
    delete: async (key: { url: string }) => stored.delete(new URL(key.url).pathname),
  };
  const asked: string[] = [];
  // Stalled fetches as they are made: the clock moves on only after them,
  // as on a slow machine they come later than the test's own steps.
  let stalls = 0;
  const waiting: [number, () => void][] = [];
  const stalled = (n: number) => new Promise<void>((resolve) => (stalls >= n ? resolve() : waiting.push([n, resolve])));
  const fetch = vi.fn((path: string, init?: { signal?: AbortSignal }) => {
    asked.push(path);
    const a = answers[path]?.shift() ?? "fail";
    if (a === "fail") return Promise.reject(new TypeError("network"));
    if (a === "stall") {
      stalls++;
      for (const [n, resolve] of waiting) if (stalls >= n) resolve();
      return new Promise<Response>((_, reject) => init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))));
    }
    return Promise.resolve(a);
  });
  const told: unknown[] = [];
  const handlers: Record<string, (e: unknown) => void> = {};
  const self = {
    addEventListener: (type: string, fn: (e: unknown) => void) => (handlers[type] = fn),
    clients: { matchAll: async () => [{ postMessage: (m: unknown) => told.push(m) }] },
    location: { origin: "https://app.example.com" },
  };
  const caches = { open: async () => cache };
  new Function("self", "caches", "fetch", source)(self, caches, fetch);
  // A page asks for the newest build; the answer comes on the port.
  const ask = () =>
    new Promise<unknown>((resolve) => {
      const work: Promise<unknown>[] = [];
      handlers.message({ data: { type: "refresh" }, ports: [{ postMessage: resolve }], waitUntil: (p: Promise<unknown>) => work.push(p) });
    });
  return { stored, asked, told, ask, stalled };
}

const html = () => new Response(PAGE, { headers: { "content-type": "text/html" } });
const js = () => new Response("x", { headers: { "content-type": "text/javascript" } });

afterEach(() => vi.useRealTimers());

it("gives a stalled file up after a while and tries it again", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const sw = load({ "/": [html()], "/assets/index-new.js": ["stall", js()], "/assets/index-new.css": [js()] });
  const answer = sw.ask();
  await sw.stalled(1);
  await vi.advanceTimersByTimeAsync(30_000);
  expect(await answer).toEqual({ ok: true });
  expect(sw.stored.has("/")).toBe(true);
  expect(sw.told).toEqual([{ type: "shell" }]);
});

it("keeps the old page when one of the new one's files cannot be had", async () => {
  const sw = load({ "/": [html()], "/assets/index-new.js": [js()], "/assets/index-new.css": ["fail", "fail", "fail"] });
  expect(await sw.ask()).toEqual({ ok: false });
  expect(sw.stored.has("/")).toBe(false);
  expect(sw.told).toEqual([]);
});

it("fetches the files side by side, and once for checks asked together", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const sw = load({ "/": [html()], "/assets/index-new.js": ["stall", js()], "/assets/index-new.css": ["stall", js()] });
  const answers = [sw.ask(), sw.ask()];
  await sw.stalled(2);
  await vi.advanceTimersByTimeAsync(30_000);
  expect(await Promise.all(answers)).toEqual([{ ok: true }, { ok: true }]);
  expect(sw.asked.filter((p) => p === "/")).toHaveLength(1);
  expect(sw.asked.filter((p) => p.startsWith("/assets/"))).toHaveLength(4);
});

it("signals foreground security sync with no visible notification and routes background security clicks", async () => {
  const handlers: Record<string, (event: any) => void> = {};
  const postMessage = vi.fn(), navigate = vi.fn(), focus = vi.fn();
  const window = { visibilityState: "visible", focused: true, postMessage, navigate, focus };
  const showNotification = vi.fn(), openWindow = vi.fn();
  const self = {
    addEventListener: (kind: string, work: (event: any) => void) => { handlers[kind] = work; },
    clients: { matchAll: async () => [window], openWindow },
    registration: { showNotification }, location: { origin: "https://app.example.com" },
  };
  new Function("self", "caches", "fetch", source)(self, {}, vi.fn());
  let work!: Promise<void>;
  const push = { data: { json: () => ({ tag: "/NOTIFY_KIND_SECURITY", body: "security" }) }, waitUntil: (p: Promise<void>) => { work = p; } };
  handlers.push(push); await work;
  expect(postMessage).toHaveBeenCalledWith({ type: "security" });
  expect(showNotification).not.toHaveBeenCalled();
  window.focused = false;
  handlers.push(push); await work;
  expect(showNotification.mock.calls[0][1].data).toEqual({ session: "", security: true });
  handlers.notificationclick({ notification: { data: showNotification.mock.calls[0][1].data, close: vi.fn() }, waitUntil: (p: Promise<void>) => { work = p; } });
  await work;
  expect(navigate).toHaveBeenCalledWith("/#/settings/security");
  expect(openWindow).not.toHaveBeenCalled();
});

it("shows push for a hidden client even when it still has a focused window", async () => {
  const handlers: Record<string, (event: any) => void> = {};
  const showNotification = vi.fn();
  const self = {
    addEventListener: (kind: string, work: (event: any) => void) => { handlers[kind] = work; },
    clients: { matchAll: async () => [{ visibilityState: "hidden", focused: true }] },
    registration: { showNotification }, location: { origin: "https://app.example.com" },
  };
  new Function("self", "caches", "fetch", source)(self, {}, vi.fn());
  let work!: Promise<void>;
  handlers.push({ data: { json: () => ({ tag: "s/NOTIFY_KIND_ATTENTION", body: "attention" }) }, waitUntil: (p: Promise<void>) => { work = p; } });
  await work;
  expect(showNotification).toHaveBeenCalledOnce();
});
