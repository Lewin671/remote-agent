import * as l10n from "./i18n";
// Web Push: subscribe this browser and choose which notifications to get.

import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { checkServer } from "./crypto";
import { getCoreHost } from "./core/host";
import { ClientService, NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import type { Remote } from "./remote";

/** A run only identifies its last execution-log notification. Never match a
 * session prefix: other unread runs may share that session. */
export async function closeScheduleNotification(run: { sessionId: string; notificationSeq: bigint | number }): Promise<void> {
  try {
    const seq = Number(run.notificationSeq);
    if (!run.sessionId || !Number.isSafeInteger(seq) || seq <= 0 ||
        typeof navigator === "undefined" || !("serviceWorker" in navigator)) return;
    const tag = `${run.sessionId}/scheduled:${seq}/NOTIFY_KIND_SCHEDULED`;
    const registration = await navigator.serviceWorker.ready;
    for (const notification of await registration.getNotifications({ tag })) {
      if (notification.tag === tag) {
        try { notification.close(); } catch { /* Continue closing other exact matches. */ }
      }
    }
  } catch { /* Unsupported, denied or failed cleanup must not affect reading. */ }
}

export interface PushPrefs {
  attention: boolean;
  turns: boolean;
  scheduled?: boolean;
}

const KEY = "ra-push";

export function pushSupported(): boolean {
  return "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

type PushState = "ok" | "default" | "denied" | "unsupported" | "ios-browser";

/** Whether push can work here: iPhones only allow it for pages added to
 * the home screen, and the browser may have refused permission. */
export function pushState(): PushState {
  const ios = /iPhone|iPad|iPod/.test(navigator.userAgent);
  const standalone = matchMedia("(display-mode: standalone)").matches || (navigator as { standalone?: boolean }).standalone === true;
  if (ios && !standalone) return "ios-browser";
  if (!pushSupported()) return "unsupported";
  if (Notification.permission === "denied") return "denied";
  return Notification.permission === "granted" ? "ok" : "default";
}

export function pushPrefs(): PushPrefs {
  try {
    return { attention: false, turns: false, scheduled: true, ...JSON.parse(localStorage.getItem(KEY) || "{}") };
  } catch {
    return { attention: false, turns: false, scheduled: true };
  }
}

export function registerWorker() {
  if ("serviceWorker" in navigator) return navigator.serviceWorker.register("/sw.js").catch(() => undefined);
}

function keyBytes(b64url: string): Uint8Array<ArrayBuffer> {
  const b64 = b64url.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((b64url.length + 3) % 4);
  return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
}

// Restoring the saved choices and changing them both read the browser's
// subscription and then write the server's. One runs at a time, in the
// order asked, so that the server ends with what was chosen last: a
// restore that began with the old choices is never what lands after them.
let turn: Promise<unknown> = Promise.resolve();
function inTurn<T>(work: () => Promise<T>): Promise<T> {
  const next = turn.then(work, work);
  turn = next.catch(() => undefined);
  return next;
}

/** Registers this browser's saved choices for the current device after
 * connecting, including when it has joined again under a new device ID. */
export function restorePushPrefs(remote: Remote): Promise<void> {
  return inTurn(async () => {
    if (!pushSupported() || Notification.permission !== "granted") return;
    // As saved when its turn comes, not when the connection opened.
    const prefs = pushPrefs();
    const kinds = [...(prefs.attention ? [NotifyKind.ATTENTION] : []), ...(prefs.turns ? [NotifyKind.TURN_ENDED] : []), NotifyKind.SECURITY, ...(prefs.scheduled !== false ? [NotifyKind.SCHEDULED] : [])];
    const current = () => {
      if (typeof (remote as { attachHost?: unknown }).attachHost === "function") {
        const c = getCoreHost(remote).viewConnection();
        return !remote.removed && !c.removed && c.connected;
      }
      const fake = remote as unknown as { removed?: boolean; conn?: string };
      return !fake.removed && fake.conn === "online";
    };
    const reg = await navigator.serviceWorker.ready;
    let sub = await reg.pushManager.getSubscription();
    if (!current()) return;
    if (!sub) {
      const key = await remote.pushKey();
      if (!current()) return;
      sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(key) });
    }
    if (current()) await remote.setPushSubscription(JSON.stringify(sub), kinds);
  });
}

/** Subscribes or unsubscribes to match prefs. */
export function setPushPrefs(remote: Remote, prefs: PushPrefs): Promise<void> {
  return inTurn(async () => {
    const reg = await navigator.serviceWorker.ready;
    let sub = await reg.pushManager.getSubscription();
    const kinds = [...(prefs.attention ? [NotifyKind.ATTENTION] : []), ...(prefs.turns ? [NotifyKind.TURN_ENDED] : []), NotifyKind.SECURITY, ...(prefs.scheduled !== false ? [NotifyKind.SCHEDULED] : [])];
    if (kinds.length === 0) {
      await remote.setPushSubscription("", []);
      await sub?.unsubscribe();
    } else {
      if (Notification.permission !== "granted" && (await Notification.requestPermission()) !== "granted") throw new Error(l10n.t("notifications.the_browser_did_not_allow_notifications"));
      if (!sub) {
        const key = await remote.pushKey();
        sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(key) });
      }
      await remote.setPushSubscription(JSON.stringify(sub), kinds);
    }
    localStorage.setItem(KEY, JSON.stringify(prefs));
  });
}

/** Creates a minimal relay push unsubscriber for exit paths without a live Remote (Rejoin / SetupReset). */
export function createStandalonePushUnsubscriber(opts: {
  server?: string;
  getToken: () => string;
  ensureToken?: () => Promise<string | undefined>;
  fetch?: typeof fetch;
}): Pick<Remote, "setPushSubscription"> {
  return {
    async setPushSubscription(subscriptionJson: string, kinds: NotifyKind[]): Promise<void> {
      const rawServer = opts.server?.trim();
      if (!rawServer) return;
      const origin = checkServer(rawServer);
      const baseUrl = import.meta.env.VITE_SAME_ORIGIN_API ? location.origin : origin;
      let token = opts.getToken();
      if (!token && opts.ensureToken) {
        token = (await opts.ensureToken()) ?? "";
      }
      if (!token) return;
      const api = createClient(
        ClientService,
        createConnectTransport({
          baseUrl,
          useBinaryFormat: true,
          defaultTimeoutMs: 4000,
          fetch: opts.fetch,
        }),
      );
      await api.setPushSubscription(
        { subscriptionJson, kinds },
        { headers: { Authorization: "Bearer " + token } },
      );
    },
  };
}

/** Best-effort push teardown before clearing local keys on exit.
 * Clears the relay push subscription (when a live remote is provided) and
 * unsubscribes the browser PushSubscription so the Service Worker stops
 * receiving notifications for an exited account. Never blocks exit indefinitely. */
export async function unregisterPushOnExit(
  remote?: Pick<Remote, "setPushSubscription">,
  timeoutMs = 5000,
): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const work = (async () => {
    if (remote) {
      try {
        await remote.setPushSubscription("", []);
      } catch {
        // Offline or already revoked; still unsubscribe locally.
      }
    }
    if (typeof navigator !== "undefined" && "serviceWorker" in navigator) {
      try {
        const reg = await navigator.serviceWorker.getRegistration();
        const sub = await reg?.pushManager?.getSubscription();
        if (sub) await sub.unsubscribe();
      } catch {
        // Best effort: never block local key erasure.
      }
    }
  })();
  try {
    await Promise.race([
      work,
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, timeoutMs);
      }),
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

/** A foreground signal carries no private material; always pull the signed
 * chain and recovery hints, including when both session switches are off. */
export function watchSecurityPush(sync: () => void): () => void {
  if (typeof navigator === "undefined" || typeof navigator.serviceWorker?.addEventListener !== "function") return () => undefined;
  const onMessage = (event: MessageEvent) => {
    if (event.data?.type === "security") sync();
  };
  navigator.serviceWorker.addEventListener("message", onMessage);
  return () => navigator.serviceWorker.removeEventListener("message", onMessage);
}
