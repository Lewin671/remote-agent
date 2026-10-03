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

// Keeps a newly fetched page, but only together with the scripts and
// styles it names: a release may replace them on the server before the
// page is next opened, and a kept page without its scripts is a blank one.
async function adopt(cache, res) {
  if (!found(res, "/")) return;
  const html = await res.clone().text();
  for (const path of new Set(html.match(/\/assets\/[\w.-]+/g) || [])) {
    const got = (await cache.match(path, any)) || (await fetch(path));
    if (!found(got, path)) return;
    await keep(cache, path, got);
  }
  await keep(cache, "/", res);
  await prune(cache);
}

async function shell(e) {
  const url = new URL(e.request.url);
  const cache = await caches.open(SHELL);
  if (e.request.mode === "navigate") {
    // Every route is the one page; the route is in the fragment.
    const kept = await cache.match("/", any);
    const fresh = fetch(new Request("/", { cache: "no-cache" }));
    // Answered from the copy kept, which is fetched again for next time.
    e.waitUntil(fresh.then((res) => adopt(cache, res.clone())).catch(() => undefined));
    return kept || fresh.then((res) => res.clone());
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
      if (windows.some((w) => w.visibilityState === "visible" && w.focused)) return;
      await self.registration.showNotification(d.title || "remote-agent", {
        body: d.body || "",
        tag: d.tag,
        renotify: true,
        icon: "/icon-192.png",
        badge: "/icon-192.png",
        data: { session: d.session || "" },
      });
    })(),
  );
});

self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  const session = e.notification.data && e.notification.data.session;
  const url = "/" + (session ? "#/s/" + session : "");
  e.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      for (const w of windows) {
        if ("focus" in w) {
          await w.focus();
          if (session && "navigate" in w) await w.navigate(url);
          return;
        }
      }
      await self.clients.openWindow(url);
    })(),
  );
});
