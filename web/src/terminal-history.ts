import type { TerminalHistoryPage } from "./gen/remoteagent/v1/remote_pb";

/** One immutable page of a visit: whole lines of a fixed capture, newest
 * first when shown. */
export interface HistoryPage {
  id: string;
  lines: string[];
}

/** idle: the live terminal. loading: a fresh capture was asked for and has
 * not arrived (or failed: error says so), so the live screen is still up.
 * reading: the reader is on screen. */
export type HistoryPhase = "idle" | "loading" | "reading";

/** How a failed request should read: the capture is gone (go back to live
 * and scroll again) or a retry may work. */
export type HistoryError = "" | "failed" | "expired";

/** One visit's pages. Replies from abandoned requests are ignored, and the
 * reader does not open at all when the capture is empty. */
export class TerminalHistory {
  phase: HistoryPhase = "idle";
  pages: HistoryPage[] = [];
  cursor = "";
  pending = "";
  error: HistoryError = "";
  closed = false;
  private timer?: ReturnType<typeof setTimeout>;
  private entering = false;
  constructor(private changed: () => void) {}

  /** The reader is on screen. */
  get visible(): boolean {
    return this.phase === "reading";
  }

  close() {
    clearTimeout(this.timer);
    this.phase = "idle";
    this.pending = "";
    this.entering = false;
    this.pages = [];
    this.cursor = "";
    this.error = "";
    this.closed = false;
    this.changed();
  }

  /** fresh starts a visit and asks for a new capture with an empty cursor;
   * otherwise it reads the page before the current cursor. */
  request(send: (id: string, cursor: string) => void, fresh = false) {
    if (fresh) {
      // A fresh capture starts a visit, or retries one whose first page failed.
      if (this.phase === "reading" || this.pending) return;
      this.phase = "loading";
      this.pages = [];
      this.cursor = "";
      this.error = "";
      this.closed = false;
      this.entering = true;
    }
    if (this.pending || (!fresh && this.pages.length && !this.cursor)) return;
    this.error = "";
    const id = crypto.randomUUID();
    this.pending = id;
    this.timer = setTimeout(() => {
      if (this.pending !== id) return;
      this.pending = "";
      this.error = "failed";
      this.entering = false;
      this.changed();
    }, 10000);
    this.changed();
    send(id, fresh ? "" : this.cursor);
  }

  receive(page: TerminalHistoryPage) {
    if (this.phase === "idle" || page.requestId !== this.pending) return;
    clearTimeout(this.timer);
    this.pending = "";
    const entering = this.entering;
    this.entering = false;
    if (page.error) {
      // A first page that fails leaves the live screen up, saying so; there is no capture to expire yet.
      this.error = !entering && page.error.includes("expired") ? "expired" : "failed";
    } else {
      this.error = "";
      if (page.lines.length) this.pages = [{ id: page.requestId, lines: page.lines }, ...this.pages];
      this.cursor = page.nextCursor;
      if (entering) this.phase = this.pages.length || this.cursor ? "reading" : "idle";
    }
    this.changed();
  }

  disconnected() {
    clearTimeout(this.timer);
    this.pending = "";
    this.entering = false;
    if (this.phase === "reading") this.closed = true;
    else if (this.phase === "loading") this.phase = "idle";
    this.changed();
  }
}
