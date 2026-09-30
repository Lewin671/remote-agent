// Retrying a command whose outcome is unknown must send exactly the same
// thing again: the same payload under the same request ID and deadline, so
// the daemon answers from its log instead of running it twice
// (docs/architecture.md §10). Anything the page learns meanwhile, such as
// the new session's own metadata, must not change what is retried.

import { CommandError, newSlot, uncertain, type RequestSlot } from "./remote";

/** The daemon refused a command as past its deadline. */
export function expiredError(e: unknown): boolean {
  return e instanceof CommandError && e.code === "deadline_exceeded";
}

/** Whether an attempt's outcome stays unknown after error. Once a
 * request's outcome was unknown, no later failure says it did not run:
 * not an expired answer (the daemon forgets a request a minute after its
 * deadline), not an offline machine or a network error. Only the daemon's
 * own answer for the request settles it. */
export function stillUnknown(error: unknown, wasUnknown: boolean): boolean {
  return uncertain(error) || (wasUnknown && (expiredError(error) || (error instanceof CommandError && error.code === "unavailable")));
}

export class Attempt<P> {
  private pending?: { payload: P; slot: RequestSlot; tries: number; expired: boolean };

  /** What to send: an earlier attempt whose outcome is unknown wins over
   * payload, and is sent sealed as it was, even past its deadline: the
   * daemon may still remember its result. */
  next(payload: P): { payload: P; slot: RequestSlot } {
    this.pending ??= { payload, slot: newSlot(), tries: 0, expired: false };
    this.pending.tries++;
    return this.pending;
  }

  /** Records how the attempt ended. Success or a definite failure frees
   * the next attempt; an unknown outcome freezes it. A retry that comes
   * back expired leaves it unknown for good: it is not sent again and no
   * new request replaces it until the user gives it up. */
  settle(error?: unknown) {
    const p = this.pending;
    if (!p) return;
    if (error !== undefined && stillUnknown(error, p.tries > 1)) {
      if (expiredError(error)) p.expired = true;
      return;
    }
    this.pending = undefined;
  }

  /** Gives up an attempt of unknown outcome, e.g. once the user has
   * checked it did not happen, or changed what to send. */
  abandon() {
    this.pending = undefined;
  }

  /** The payload of the attempt of unknown outcome, if any. */
  get payload(): P | undefined {
    return this.pending?.payload;
  }

  /** Whether the next attempt repeats an unknown one. */
  get frozen(): boolean {
    return this.pending !== undefined;
  }

  /** Whether the unknown attempt has expired: it cannot be sent again. */
  get expired(): boolean {
    return this.pending?.expired ?? false;
  }
}
