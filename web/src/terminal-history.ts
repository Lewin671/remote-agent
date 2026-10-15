import type { TerminalHistoryPage } from "./gen/remoteagent/v1/remote_pb";

/** One immutable capture per visit. Replies from abandoned requests are ignored. */
export class TerminalHistory {
  visible = false;
  pages: { id: string; text: string }[] = [];
  cursor = "";
  pending = "";
  error = "";
  private timer?: ReturnType<typeof setTimeout>;
  constructor(private changed: () => void) {}
  reset() { this.close(); this.visible = true; this.pages = []; this.cursor = ""; this.error = ""; }
  close() { clearTimeout(this.timer); this.pending = ""; this.visible = false; this.pages = []; this.cursor = ""; this.error = ""; this.changed(); }
  request(send: (id: string, cursor: string) => void, fresh = false) {
    if (fresh) this.reset();
    if (!this.visible || this.pending || (!fresh && this.pages.length && !this.cursor)) return;
    this.error = "";
    const id = crypto.randomUUID();
    this.pending = id;
    this.timer = setTimeout(() => { if (this.pending === id) { this.pending = ""; this.error = "读取历史超时，请重试"; this.changed(); } }, 10000);
    this.changed();
    send(id, fresh ? "" : this.cursor);
  }
  receive(page: TerminalHistoryPage) {
    if (!this.visible || page.requestId !== this.pending) return;
    clearTimeout(this.timer);
    this.pending = "";
    this.error = page.error ? "历史读取失败，请重试；若历史已过期，请返回终端后重新打开" : "";
    if (!page.error) {
      if (page.lines.length) this.pages = [{ id: page.requestId, text: page.lines.join("\n") }, ...this.pages];
      this.cursor = page.nextCursor;
    }
    this.changed();
  }
  disconnected() {
    clearTimeout(this.timer); this.pending = "";
    if (this.visible) this.error = "终端连接已断开，请返回终端并重新连接";
    this.changed();
  }
}
