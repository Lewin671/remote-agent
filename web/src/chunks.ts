// Parts of the page that are fetched when first used (a settings page, the
// terminal), and what to do when one of them is not there: the server
// has moved on to a newer build and no longer has the old one's files.

let ahead = false;

/** The kept copy of the page is dropped, so that the service worker does
 * not answer with the build that has gone; and if the part was needed now,
 * the page starts over on the current build (not twice within a minute).
 * Whether it is starting over. */
export async function chunkMissing(needed: boolean): Promise<boolean> {
  const last = Number(sessionStorage.getItem("ra-reloaded"));
  try {
    await (await caches.open("shell-v1")).delete("/", { ignoreVary: true });
  } catch {
    // No cache storage: the network answers anyway.
  }
  if (!needed || Date.now() - last < 60_000) return false;
  sessionStorage.setItem("ra-reloaded", String(Date.now()));
  location.reload();
  return true;
}

/** A chunk that fails to load belongs to a build the server has replaced. A
 * part that was only being fetched ahead interrupts nothing: the next
 * visit, or the first need, moves on. */
export function watchChunks() {
  addEventListener("vite:preloadError", (e) => {
    e.preventDefault();
    void chunkMissing(!ahead);
  });
}

/** Fetches parts ahead, one at a time while the page is idle and once it
 * has settled, so that they are at hand later: offline, after the server
 * has moved on to a newer build, or simply without a wait when a page is
 * opened. */
export function fetchAhead(parts: (() => Promise<unknown>)[], after = 4000) {
  const idle = () =>
    new Promise<void>((done) => ("requestIdleCallback" in window ? requestIdleCallback(() => done(), { timeout: 3000 }) : setTimeout(done, 200)));
  setTimeout(() => {
    ahead = true;
    void (async () => {
      for (const part of parts) {
        await idle();
        await part().catch(() => undefined);
      }
      ahead = false;
    })();
  }, after);
}
