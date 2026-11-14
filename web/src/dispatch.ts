import { RESULT_RETENTION_MS } from "./result-retention.gen";

/** Evidence belongs to an authorization; each call and each delivery has an owner. */
export class DispatchState {
  private possible = 0;
  private active = 0;
  constructor(private prior: boolean) {}
  includePrior(prior: boolean) { this.prior ||= prior; }
  get hasActiveCalls() { return this.active > 0; }
  get unresolved() { return this.prior || this.possible > 0 || this.active > 0; }
  open() {
    this.active++;
    let closed = false;
    const state = this;
    return {
      get hasPrior() { return state.prior || state.possible > 0; },
      get uncertain() { return state.prior || state.possible > 0 || state.active > (closed ? 0 : 1); },
      close() { if (!closed) { closed = true; state.active--; } },
      delivery() {
        let registered = false, settled = false;
        return {
          dispatch() {
            if (registered || settled || closed) throw new Error("dispatch_handle_closed");
            registered = true; state.possible++;
          },
          notForwarded() {
            if (!settled) { settled = true; if (registered) state.possible--; }
          },
        };
      },
    };
  }
}
export type DispatchCall = ReturnType<DispatchState["open"]>;


const REAP_INTERVAL_MS = 30_000;
type Clock = () => { wall: number; elapsed: number };
type Schedule = (run: () => void) => () => void;
function scheduleReap(run: () => void) {
  const timer = setTimeout(run, REAP_INTERVAL_MS);
  // Node tests need not stay alive for a browser's idle metadata cleanup.
  (timer as unknown as { unref?: () => void }).unref?.();
  return () => clearTimeout(timer);
}

/** Contains metadata only, never a Remote, payload, or credential. A timer may
 * outlive a stopped Remote to expire its inactive entries without keeping it alive. */
export class DispatchRegistry {
  private readonly entries = new Map<string, { state: DispatchState; until: number; elapsedUntil: number }>();
  private floor = 0;
  private cancel?: () => void;
  private generation = 0;
  constructor(
    private readonly clock: Clock = () => ({ wall: Date.now(), elapsed: performance.now() }),
    private readonly schedule: Schedule = scheduleReap,
  ) {}
  get size() { return this.entries.size; }
  get(id: string, deadline: number, state: DispatchState, prior: boolean): DispatchState {
    this.sweep();
    const now = this.clock(), until = deadline + RESULT_RETENTION_MS;
    this.floor = Math.max(this.floor, now.wall);
    const old = this.entries.get(id);
    if (old) {
      state = old.state;
      if (Number.isSafeInteger(until) && until > old.until) {
        old.until = until;
        old.elapsedUntil = Math.max(old.elapsedUntil, now.elapsed + Math.max(0, until - now.wall));
      }
    } else if (Number.isSafeInteger(until) && deadline > 0 && until > this.floor) {
      this.entries.set(id, { state, until, elapsedUntil: now.elapsed + (until - now.wall) });
    }
    // The slot keeps its own evidence after its index expires; restored slots
    // still supply explicit prior. Never reset that object while reaping keys.
    state.includePrior(prior);
    this.arm();
    return state;
  }
  sweep() {
    const now = this.clock();
    this.floor = Math.max(this.floor, now.wall);
    for (const [id, entry] of this.entries) {
      if (!entry.state.hasActiveCalls && now.wall >= entry.until && now.elapsed >= entry.elapsedUntil) this.entries.delete(id);
    }
    if (!this.entries.size && this.cancel) {
      this.generation++;
      this.cancel(); this.cancel = undefined;
    }
    this.arm();
  }
  private arm() {
    if (this.cancel || !this.entries.size) return;
    const generation = ++this.generation;
    this.cancel = this.schedule(() => {
      if (this.generation !== generation) return;
      this.cancel = undefined;
      this.sweep();
    });
  }
}
