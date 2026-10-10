// Web Push: subscribe this browser and choose which notifications to get.

import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import type { Remote } from "./remote";

export interface PushPrefs {
  attention: boolean;
  turns: boolean;
}

const KEY = "ra-push";

export function pushSupported(): boolean {
  return "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

export type PushState = "ok" | "default" | "denied" | "unsupported" | "ios-browser";

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
    return { attention: false, turns: false, ...JSON.parse(localStorage.getItem(KEY) || "{}") };
  } catch {
    return { attention: false, turns: false };
  }
}

export function registerWorker() {
  if ("serviceWorker" in navigator) navigator.serviceWorker.register("/sw.js").catch(() => undefined);
}

function keyBytes(b64url: string): Uint8Array<ArrayBuffer> {
  const b64 = b64url.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((b64url.length + 3) % 4);
  return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
}

/** Registers this browser's saved choices for the current device after
 * connecting, including when it has joined again under a new device ID. */
export async function restorePushPrefs(remote: Remote): Promise<void> {
  if (!pushSupported() || Notification.permission !== "granted") return;
  const prefs = pushPrefs();
  const kinds = [...(prefs.attention ? [NotifyKind.ATTENTION] : []), ...(prefs.turns ? [NotifyKind.TURN_ENDED] : [])];
  if (!kinds.length) return;
  const current = () => !remote.removed && remote.conn === "online";
  const reg = await navigator.serviceWorker.ready;
  let sub = await reg.pushManager.getSubscription();
  if (!current()) return;
  if (!sub) {
    const key = await remote.pushKey();
    if (!current()) return;
    sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(key) });
  }
  if (current()) await remote.setPushSubscription(JSON.stringify(sub), kinds);
}

/** Subscribes or unsubscribes to match prefs. */
export async function setPushPrefs(remote: Remote, prefs: PushPrefs): Promise<void> {
  const reg = await navigator.serviceWorker.ready;
  let sub = await reg.pushManager.getSubscription();
  const kinds = [...(prefs.attention ? [NotifyKind.ATTENTION] : []), ...(prefs.turns ? [NotifyKind.TURN_ENDED] : [])];
  if (kinds.length === 0) {
    await remote.setPushSubscription("", []);
    await sub?.unsubscribe();
  } else {
    if ((await Notification.requestPermission()) !== "granted") throw new Error("浏览器没有允许通知");
    if (!sub) {
      const key = await remote.pushKey();
      sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(key) });
    }
    await remote.setPushSubscription(JSON.stringify(sub), kinds);
  }
  localStorage.setItem(KEY, JSON.stringify(prefs));
}
