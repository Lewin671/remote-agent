import { Recovery } from "./recovery";

export const TERMINAL_STABILITY_MS = 5_000;

/**
 * Coordinates per-stream reopen backoff, process-global token budgeting, and
 * offline/online transitions for a single terminal stream.
 */
export class TerminalReconnectController {
  readonly recovery: Recovery;
  private closedNeedsReopen = false;
  private reopenedInStreak = false;
  private retryTimer?: ReturnType<typeof setTimeout>;
  private stableTimer?: ReturnType<typeof setTimeout>;
  private disposed = false;

  constructor(
    private isOnline: () => boolean,
    private triggerOpen: () => void,
    recovery?: Recovery,
  ) {
    this.recovery = recovery ?? new Recovery();
  }

  /** Marks a terminal that could not open yet because the main link is offline. */
  markOfflineClosed() {
    if (this.disposed) return;
    clearTimeout(this.stableTimer);
    this.stableTimer = undefined;
    this.closedNeedsReopen = true;
  }

  /** Starts the 5s stability timer once the stream is live with its snapshot. */
  onLive() {
    if (this.disposed) return;
    this.closedNeedsReopen = false;
    clearTimeout(this.stableTimer);
    this.stableTimer = setTimeout(() => {
      this.stableTimer = undefined;
      if (this.disposed) return;
      this.reopenedInStreak = false;
      this.recovery.ready();
    }, TERMINAL_STABILITY_MS);
  }

  /**
   * Handles stream closure or opening failure (decryption error, out-of-order,
   * >2 MiB backlog, daemon Closed, or open rejection).
   */
  onClosed(opened: boolean) {
    if (this.disposed) return;
    clearTimeout(this.stableTimer);
    this.stableTimer = undefined;
    this.closedNeedsReopen = true;
    if (this.reopenedInStreak) {
      this.recovery.failed();
    }
    if (!opened || !this.isOnline()) return;
    this.tryTakeOrSchedule();
  }

  /** Reopens a closed terminal when the main link transitions to online. */
  onMainOnline() {
    if (this.disposed || !this.closedNeedsReopen || !this.isOnline()) return;
    this.tryTakeOrSchedule();
  }

  /** Explicit user reconnect resets the backoff sequence and opens immediately. */
  manualReconnect() {
    if (this.disposed) return;
    clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    clearTimeout(this.stableTimer);
    this.stableTimer = undefined;
    this.closedNeedsReopen = false;
    this.reopenedInStreak = false;
    this.recovery.ready();
    this.triggerOpen();
  }

  private tryTakeOrSchedule() {
    if (this.disposed || !this.closedNeedsReopen || !this.isOnline()) return;
    if (this.retryTimer !== undefined) return;
    const delay = Math.ceil(this.recovery.delay());
    if (delay === 0 && this.recovery.take()) {
      this.closedNeedsReopen = false;
      this.reopenedInStreak = true;
      this.triggerOpen();
      return;
    }
    this.retryTimer = setTimeout(() => {
      this.retryTimer = undefined;
      this.tryTakeOrSchedule();
    }, Math.max(1, delay));
  }

  dispose() {
    this.disposed = true;
    clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    clearTimeout(this.stableTimer);
    this.stableTimer = undefined;
  }
}
