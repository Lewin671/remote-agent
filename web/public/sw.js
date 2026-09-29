// Service worker: shows push notifications and opens their session.
// Notifications carry generic text only; the content stays encrypted.

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));

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
