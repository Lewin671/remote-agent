// Service worker: shows push notifications and opens their session.
// Notifications carry generic text only; the content stays encrypted.

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));

// The app shell is kept here, so that opening the app does not wait for
// the network: the page as last fetched is answered at once and fetched
// again in the background for next time. Its scripts and styles carry a
// content hash in their names and never change. Nothing of the API goes
// through here: that is another origin.
const SHELL = "shell-v1";

// How many scripts and styles are kept: a few builds' worth, so that a tab
// still running an earlier build finds its own.
const ASSETS = 40;

// Files are kept and found by path alone: the same file is asked for by
// a script tag, by an import and from here, with different request headers.
const any = { ignoreVary: true, ignoreSearch: true };

async function keep(cache, key, res) {
  const headers = new Headers(res.headers);
  headers.set("x-kept-at", String(Date.now()));
  await cache.put(key, new Response(await res.clone().blob(), { status: res.status, statusText: res.statusText, headers }));
}

// A file that is really there: the host answers a path it does not have
// with the page itself.
function found(res, path) {
  // "default" is a copy kept here; anything else came from elsewhere.
  return res.ok && (res.type === "basic" || res.type === "default") && (path === "/" || !/text\/html/.test(res.headers.get("content-type") || ""));
}

async function prune(cache) {
  const assets = [];
  for (const req of await cache.keys()) {
    if (!new URL(req.url).pathname.startsWith("/assets/")) continue;
    const res = await cache.match(req, any);
    assets.push({ req, at: Number(res && res.headers.get("x-kept-at")) || 0 });
  }
  assets.sort((a, b) => b.at - a.at);
  for (const a of assets.slice(ASSETS)) await cache.delete(a.req);
}

// How long one file may take and how many tries it gets: the way to the
// host is slow and now and then stalls, and a fetch that never ends would
// hold the new page back for good.
const WAIT = 30_000;
const TRIES = 3;

// A file read in full within the time allowed, or an error.
async function get(path, init) {
  for (let i = 1; ; i++) {
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(), WAIT);
    try {
      const res = await fetch(path, { ...init, signal: ctl.signal });
      const body = await res.blob();
      return new Response(body, { status: res.status, statusText: res.statusText, headers: res.headers });
    } catch (err) {
      if (i >= TRIES) throw err;
    } finally {
      clearTimeout(timer);
    }
  }
}

// Keeps a newly fetched page, but only together with the scripts and
// styles it names: a release may replace them on the server before the
// page is next opened, and a kept page without its scripts is a blank one.
// They are fetched side by side, and the page is kept once all are in.
async function adopt(cache, res) {
  if (!found(res, "/")) return false;
  const html = await res.clone().text();
  const files = await Promise.all(
    [...new Set(html.match(/\/assets\/[\w.-]+/g) || [])].map(async (path) => {
      const got = (await cache.match(path, any)) || (await get(path));
      if (!found(got, path)) throw new Error("not found: " + path);
      return [path, got];
    }),
  );
  for (const [path, got] of files) await keep(cache, path, got);
  await keep(cache, "/", res);
  await prune(cache);
  return true;
}

// Fetches the page again and keeps it for next time; one at a time.
// The open pages are told when it is in, so that they can offer it.
let refreshing = null;
function refresh() {
  refreshing ||= (async () => {
    try {
      const cache = await caches.open(SHELL);
      if (!(await adopt(cache, await get("/", { cache: "no-cache" })))) return false;
      for (const w of await self.clients.matchAll({ type: "window" })) w.postMessage({ type: "shell" });
      return true;
    } finally {
      refreshing = null;
    }
  })();
  return refreshing;
}

async function shell(e) {
  const url = new URL(e.request.url);
  const cache = await caches.open(SHELL);
  if (e.request.mode === "navigate") {
    // Every route is the one page; the route is in the fragment. It is
    // answered from the copy kept, which is fetched again for next time.
    e.waitUntil(refresh().catch(() => undefined));
    return (await cache.match("/", any)) || fetch(new Request("/", { cache: "no-cache" }));
  }
  const kept = await cache.match(url.pathname, any);
  const hashed = url.pathname.startsWith("/assets/");
  if (kept && hashed) return kept;
  const fresh = fetch(e.request).then(async (res) => {
    if (found(res, url.pathname)) {
      await keep(cache, url.pathname, res);
      if (hashed) await prune(cache);
    }
    return res;
  });
  if (!kept) return fresh;
  e.waitUntil(fresh.catch(() => undefined));
  return kept;
}

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  if (e.request.method !== "GET" || url.origin !== self.location.origin) return;
  const kept = (e.request.mode === "navigate" && url.pathname === "/") || /^\/(assets|fonts)\//.test(url.pathname) || /^\/[\w.-]+\.(png|webmanifest)$/.test(url.pathname);
  if (!kept) return;
  // Whatever goes wrong here, the network still answers.
  e.respondWith(shell(e).catch(() => fetch(e.request)));
});

// A page asks for the newest build now: the answer says whether it is kept.
self.addEventListener("message", (e) => {
  if (e.data?.type !== "refresh" || !e.ports[0]) return;
  const port = e.ports[0];
  e.waitUntil(refresh().then((ok) => port.postMessage({ ok }), () => port.postMessage({ ok: false })));
});

self.addEventListener("push", (e) => {
  let d = {};
  try {
    d = e.data ? e.data.json() : {};
  } catch {
    d = {};
  }
  e.waitUntil(
    (async () => {
      // Someone is looking at the app already.
      const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      const security = typeof d.tag === "string" && d.tag.endsWith("/NOTIFY_KIND_SECURITY");
      if (security) for (const w of windows) w.postMessage({ type: "security" });
      if (windows.some((w) => w.visibilityState === "visible" && w.focused)) return;
      await self.registration.showNotification(d.title || "remote-agent", {
        body: d.body || "",
        tag: d.tag,
        renotify: true,
        icon: "/icon-192.png",
        badge: "/icon-192.png",
        data: { session: d.session || "", security },
      });
    })(),
  );
});

self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  const session = e.notification.data && e.notification.data.session;
  const security = e.notification.data?.security === true;
  const url = security ? "/#/settings/security" : "/" + (session ? "#/s/" + session : "");
  e.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      for (const w of windows) {
        if ("focus" in w) {
          await w.focus();
          if ((security || session) && "navigate" in w) await w.navigate(url);
          return;
        }
      }
      await self.clients.openWindow(url);
    })(),
  );
});
