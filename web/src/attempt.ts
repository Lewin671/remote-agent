// Retrying a command whose outcome is unknown must send exactly the same
// thing again: the same payload under the same request ID and deadline, so
// the daemon answers from its log instead of running it twice
// (docs/architecture.md §10). Anything the page learns meanwhile, such as
// the new session's own metadata, must not change what is retried.

import { newSlot, uncertain, type RequestSlot } from "./remote";

export class Attempt<P> {
  private pending?: { payload: P; slot: RequestSlot };

  /** What to send: an earlier attempt whose outcome is unknown wins over
   * payload. */
  next(payload: P): { payload: P; slot: RequestSlot } {
    return (this.pending ??= { payload, slot: newSlot() });
  }

  /** Records how the attempt ended: success or a definite failure frees
   * the next attempt; an unknown outcome freezes it. */
  settle(error?: unknown) {
    if (error === undefined || !uncertain(error)) this.pending = undefined;
  }

  /** Whether the next attempt repeats an unknown one. */
  get frozen(): boolean {
    return this.pending !== undefined;
  }
}
