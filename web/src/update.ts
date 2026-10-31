// Moving on to a newer build. The service worker keeps the page last
// fetched and answers with it (src/sw.js); a newer one is in once the
// kept page runs another script than this one. Then reloading starts it.

import { useSyncExternalStore } from "react";
import { registerWorker } from "./push";

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
async function refresh(signal: AbortSignal): Promise<boolean> {
  // A failed registration is an answer, unlike serviceWorker.ready which
  // can wait forever. Register again so the next check can recover.
  const registration = await registerWorker();
  if (!registration || signal.aborted) return false;
  const worker = registration.active ?? (await navigator.serviceWorker.ready).active;
  if (!worker || signal.aborted) return false;
  return new Promise((resolve) => {
    const ch = new MessageChannel();
    const finish = (ok: boolean) => {
      signal.removeEventListener("abort", abort);
      ch.port1.close();
      ch.port2.close();
      resolve(ok);
    };
    const abort = () => finish(false);
    signal.addEventListener("abort", abort, { once: true });
    ch.port1.onmessage = (e) => finish(e.data?.ok === true);
    try {
      worker.postMessage({ type: "refresh" }, [ch.port2]);
    } catch {
      finish(false);
    }
  });
}

let checking = false;

/** Checks for a newer build. The deadline covers registration, activation,
 * the worker reply and reading the kept page. Late results cannot change
 * the outcome of a timed out check or a subsequent retry. */
export async function checkUpdate(asked = false): Promise<void> {
  if (state === "ready" || checking || !("serviceWorker" in navigator)) return;
  checking = true;
  if (asked) set("checking");
  const controller = new AbortController();
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    const result = await Promise.race([
      (async (): Promise<UpdateState> => {
        const ok = await refresh(controller.signal);
        return await newer() ? "ready" : ok ? "latest" : "failed";
      })().catch(() => "failed" as const),
      new Promise<UpdateState>((resolve) => { timer = setTimeout(() => resolve("failed"), ANSWER); }),
    ]);
    // A shell message may already have found an update during the check.
    if ((state as UpdateState) !== "ready" && (asked || result === "ready")) set(result);
  } finally {
    clearTimeout(timer);
    controller.abort();
    checking = false;
  }
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
