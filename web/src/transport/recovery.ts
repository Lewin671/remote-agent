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
const processBudget = new Bucket(4, 2);
export class PathChanged extends Error {}

export class Recovery {
  private target: Bucket;
  private failures = 0;
  private retryAt = 0;
  private listeners = new Set<() => void>();
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
    for (const wake of [...this.listeners]) wake();
  }
  /** A new authenticated path retires only this HTTP waiter. Its delivery
   * evidence survives in Link; the next attempt uses the original envelope. */
  async follow<T>(send: (signal: AbortSignal) => Promise<T>, parent?: AbortSignal): Promise<T> {
    const controller = new AbortController();
    const abort = () => controller.abort(parent?.reason);
    parent?.addEventListener("abort", abort, { once: true });
    if (parent?.aborted) abort();
    let wake!: () => void;
    const changed = new Promise<never>((_, reject) => {
      wake = () => { reject(new PathChanged()); controller.abort(); };
      this.listeners.add(wake);
    });
    try { return await Promise.race([send(controller.signal), changed]); }
    finally { this.listeners.delete(wake); parent?.removeEventListener("abort", abort); }
  }
}
