// Moving on to a newer build. The service worker keeps the page last
// fetched and answers with it (public/sw.js); a newer one is in once the
// kept page runs another script than this one. Then reloading starts it.

import { useSyncExternalStore } from "react";

export type UpdateState = "idle" | "checking" | "latest" | "ready" | "failed";

let state: UpdateState = "idle";
const listeners = new Set<() => void>();

function set(next: UpdateState) {
  state = next;
  for (const l of listeners) l();
}

export function useUpdate(): UpdateState {
  return useSyncExternalStore(
    (notify) => {
      listeners.add(notify);
      return () => listeners.delete(notify);
    },
    () => state,
  );
}

const ENTRY = /<script[^>]*\ssrc="(\/assets\/[^"]+\.js)"/;

/** The script this page runs; none in development, where nothing is kept. */
function ownEntry(): string | undefined {
  const src = document.querySelector<HTMLScriptElement>('script[type="module"][src^="/assets/"]')?.getAttribute("src");
  return src ?? undefined;
}

async function keptEntry(): Promise<string | undefined> {
  const res = await (await caches.open("shell-v1")).match("/", { ignoreVary: true, ignoreSearch: true });
  return res ? (await res.text()).match(ENTRY)?.[1] : undefined;
}

/** Whether the kept page is another build than this one. */
async function newer(): Promise<boolean> {
  const own = ownEntry();
  if (!own) return false;
  const kept = await keptEntry().catch(() => undefined);
  return kept !== undefined && kept !== own;
}

// Longer than the service worker can take: its tries at the page and at
// its files, one after the other.
const ANSWER = 200_000;

/** Asks the service worker to fetch the newest build now. */
async function refresh(): Promise<boolean> {
  const worker = (await navigator.serviceWorker.ready).active;
  if (!worker) return false;
  return new Promise((resolve) => {
    const ch = new MessageChannel();
    const timer = setTimeout(() => resolve(false), ANSWER);
    ch.port1.onmessage = (e) => {
      clearTimeout(timer);
      resolve(e.data?.ok === true);
    };
    worker.postMessage({ type: "refresh" }, [ch.port2]);
  });
}

/** Checks for a newer build. Asked for, the outcome is shown either way;
 * otherwise only one found. */
export async function checkUpdate(asked = false): Promise<void> {
  if (state === "ready" || state === "checking" || !("serviceWorker" in navigator)) return;
  if (asked) set("checking");
  const ok = await refresh().catch(() => false);
  if (await newer()) set("ready");
  else if (asked) set(ok ? "latest" : "failed");
}

export function applyUpdate() {
  location.reload();
}

// Checked again on coming back to the page, at most this often: an
// installed app is mostly brought back, not opened anew.
const AGAIN = 10 * 60_000;

export function watchUpdates() {
  if (!("serviceWorker" in navigator)) return;
  // The service worker says when it has kept a page.
  navigator.serviceWorker.addEventListener("message", (e) => {
    if (e.data?.type === "shell") void newer().then((yes) => yes && set("ready"));
  });
  let last = Date.now();
  document.addEventListener("visibilitychange", () => {
    if (document.hidden || Date.now() - last < AGAIN) return;
    last = Date.now();
    void checkUpdate();
  });
}
