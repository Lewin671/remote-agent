import type { Remote } from "../remote";

export type NetworkConnection = EventTarget & { type?: string };

/**
 * Computes a network identity signature from only stable quantities
 * (navigator.onLine and connection.type), ignoring volatile transport metrics
 * like rtt, downlink, effectiveType, and saveData.
 */
export function stableNetworkSignature(onLine: boolean, connection?: { type?: string }): string {
  return JSON.stringify([Boolean(onLine), connection?.type ?? ""]);
}

/**
 * Coalesces online, offline, and NetworkInformation change events triggered by
 * the same physical network event into a single microtask batch.
 */
export function bindNetworkChanges(
  remote: Pick<Remote, "setNetwork">,
  nav: { onLine: boolean; connection?: NetworkConnection },
  win: Pick<Window, "addEventListener" | "removeEventListener">,
  isRunning: () => boolean = () => true,
) {
  let disposed = false;
  let queued = false;
  const report = () => {
    if (disposed || !isRunning()) return;
    remote.setNetwork(nav.onLine, stableNetworkSignature(nav.onLine, nav.connection));
  };
  const schedule = () => {
    if (disposed || !isRunning() || queued) return;
    queued = true;
    queueMicrotask(() => {
      queued = false;
      report();
    });
  };
  const connection = nav.connection;
  connection?.addEventListener("change", schedule);
  win.addEventListener("online", schedule);
  win.addEventListener("offline", schedule);
  return {
    report,
    dispose() {
      disposed = true;
      connection?.removeEventListener("change", schedule);
      win.removeEventListener("online", schedule);
      win.removeEventListener("offline", schedule);
    },
  };
}
