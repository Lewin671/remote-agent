// Retrying a command whose outcome is unknown must send exactly the same
// thing again: the same payload under the same request ID and deadline, so
// the daemon answers from its log instead of running it twice
// (docs/architecture.md §10). Anything the page learns meanwhile, such as
// the new session's own metadata, must not change what is retried.

import { CommandError, newSlot, uncertain, type RequestSlot } from "./remote";

/** This device stopped dispatching a request past its deadline and clock allowance. */
function expiredError(e: unknown): boolean {
  return e instanceof CommandError && e.outcome === "not_sent" && e.code === "expired";
}

/** Whether an attempt's outcome stays unknown after error. Once a
 * request's outcome was unknown, no later failure says it did not run:
 * not a locally expired request (the daemon may forget it after its
 * deadline), not an offline machine or a network error. Only the daemon's
 * own answer for the request settles it. */
export function stillUnknown(error: unknown, wasUnknown: boolean): boolean {
  if (!(error instanceof CommandError)) return wasUnknown || uncertain(error);
  return uncertain(error) || (wasUnknown && error.outcome === "not_sent");
}

export class Attempt<P> {
  private pending?: {
    payload: P;
    slot: RequestSlot;
    tries: number;
    expired: boolean;
  };

  /** What to send: an earlier attempt whose outcome is unknown wins over
   * payload, and is sent sealed as it was while the local deadline allowance
   * permits: the daemon may still remember its result. */
  next(payload: P): { payload: P; slot: RequestSlot } {
    this.pending ??= { payload, slot: newSlot(), tries: 0, expired: false };
    this.pending.tries++;
    return this.pending;
  }

  /** Records how the attempt ended. Success or a definite failure frees
   * the next attempt; an unknown outcome freezes it. A retry that comes
   * back expired leaves it unknown for good: it is not sent again and no
   * new request replaces it until the user gives it up. sent: what next()
   * gave for the request that ended. An answer to a request that was
   * given up meanwhile says nothing about the one that replaced it. */
  settle(error?: unknown, sent?: { slot: RequestSlot }) {
    const p = this.pending;
    if (!p || (sent && sent !== p)) return;
    const final = error instanceof CommandError && error.fromDaemon && error.outcome !== "not_sent" && !uncertain(error);
    if (error !== undefined && (stillUnknown(error, p.tries > 1) || p.slot.evidence.unresolved && !final)) {
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
