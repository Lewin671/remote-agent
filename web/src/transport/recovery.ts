import { Code, ConnectError } from "@connectrpc/connect";
import * as l10n from "../i18n";

export class PathChanged extends Error {}

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
      return Reflect.apply(method, target, [request, { ...options, signal: options?.signal ? AbortSignal.any([signal, options.signal]) : signal }]);
    };
  }});
}
