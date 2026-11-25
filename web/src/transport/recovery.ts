import { Code, ConnectError } from "@connectrpc/connect";
import * as l10n from "../i18n";

export class PathChanged extends Error {}

/** Combines multiple AbortSignals with a fallback for Safari < 17.4 (lacking AbortSignal.any). */
export function combineSignals(signals: Array<AbortSignal | undefined>): { signal: AbortSignal; dispose: () => void } {
  const active = signals.filter((s): s is AbortSignal => s !== undefined);
  if (active.length === 1) return { signal: active[0]!, dispose: () => {} };
  if (typeof AbortSignal.any === "function") {
    return { signal: AbortSignal.any(active), dispose: () => {} };
  }
  const controller = new AbortController();
  for (const s of active) {
    if (s.aborted) {
      controller.abort(s.reason);
      return { signal: controller.signal, dispose: () => {} };
    }
  }
  const onAbort = (event: Event) => {
    dispose();
    controller.abort((event.target as AbortSignal).reason);
  };
  const dispose = () => {
    for (const s of active) s.removeEventListener("abort", onAbort);
  };
  for (const s of active) s.addEventListener("abort", onAbort, { once: true });
  return { signal: controller.signal, dispose };
}

/** Cancel one waiter without canceling a shared credential/security owner. */
export async function abortable<T>(operation: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return operation;
  signal.throwIfAborted();
  let abort!: () => void;
  const canceled = new Promise<never>((_, reject) => {
    abort = () => reject(signal.reason);
    signal.addEventListener("abort", abort, { once: true });
  });
  try { return await Promise.race([operation, canceled]); }
  finally { signal.removeEventListener("abort", abort); }
}

/** An operation owner sets the deadline; callers may cancel only their wait. */
export async function ownedRound<T>(owner: AbortController, ms: number, run: (signal: AbortSignal) => Promise<T>): Promise<T> {
  const timer = setTimeout(() => owner.abort(new ConnectError(l10n.t("errors.connection_timed_out"), Code.DeadlineExceeded)), ms);
  try { return await abortable(Promise.resolve().then(() => run(owner.signal)), owner.signal); }
  finally { clearTimeout(timer); }
}

/** Carry the round's cancellation through all RPCs, including metadata CAS retries. */
export function scopedClient<T extends object>(client: T, signal: AbortSignal): T {
  return new Proxy(client, { get(target, key) {
    const method = Reflect.get(target, key);
    if (typeof method !== "function") return method;
    return (request: unknown, options?: { signal?: AbortSignal }) => {
      signal.throwIfAborted();
      if (!options?.signal) {
        return Reflect.apply(method, target, [request, { ...options, signal }]);
      }
      const combined = combineSignals([signal, options.signal]);
      try {
        const result = Reflect.apply(method, target, [request, { ...options, signal: combined.signal }]);
        if (result && typeof (result as PromiseLike<unknown>).then === "function") {
          void (result as Promise<unknown>).then(combined.dispose, combined.dispose);
        } else {
          combined.dispose();
        }
        return result;
      } catch (err) {
        combined.dispose();
        throw err;
      }
    };
  }});
}
