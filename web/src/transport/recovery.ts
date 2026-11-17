import { Code, ConnectError } from "@connectrpc/connect";
import * as l10n from "../i18n";
/** Monotonic, process-local recovery budgets; hints never reset credit/backoff. */
export class Bucket {
  private tokens: number;
  private at?: number;
  constructor(private capacity: number, private rate: number, private now = () => performance.now()) {
    this.tokens = capacity;
  }
  delay() {
    const at = this.now();
    this.at ??= at;
    this.tokens = Math.min(this.capacity, this.tokens + Math.max(0, at - this.at) * this.rate / 1000);
    this.at = Math.max(at, this.at);
    return Math.max(0, (1 - this.tokens) * 1000 / this.rate);
  }
  take() { this.tokens--; }
}
let processBudget = new Bucket(4, 2);
export function resetProcessBudgetForTest() { processBudget = new Bucket(4, 2); }
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

export class Recovery {
  private target: Bucket;
  private failures = 0;
  private retryAt = 0;
  revision = 0;
  constructor(private now = () => performance.now(), private random = Math.random, private global = processBudget) {
    this.target = new Bucket(2, 1, now);
  }
  delay() { return Math.max(this.target.delay(), this.global.delay(), this.retryAt - this.now(), 0); }
  take() {
    if (this.delay() > 0) return false;
    this.target.take(); this.global.take(); return true;
  }
  failed() { this.retryAt = this.now() + this.random() * Math.min(1000 * 2 ** Math.min(this.failures++, 5), 30_000); }
  ready() {
    this.failures = 0; this.retryAt = 0; this.revision++;
  }
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
