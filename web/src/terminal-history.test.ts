import { afterEach, expect, it, vi } from "vitest";
import { TerminalHistory } from "./terminal-history";
import { create } from "@bufbuild/protobuf";
import { TerminalHistoryPageSchema } from "./gen/remoteagent/v1/remote_pb";
afterEach(() => vi.useRealTimers());
it("prepends immutable pages, prevents duplicate requests and ignores abandoned replies", () => {
  const h = new TerminalHistory(vi.fn()); const send = vi.fn();
  h.request(send, true); const first = h.pending;
  h.request(send); expect(send).toHaveBeenCalledTimes(1);
  h.receive(create(TerminalHistoryPageSchema, { requestId: first, lines: ["new 中文"], nextCursor: "capture:200" }));
  h.request(send); const second = h.pending;
  expect(send).toHaveBeenLastCalledWith(second, "capture:200");
  h.receive(create(TerminalHistoryPageSchema, { requestId: second, lines: ["old"], nextCursor: "" }));
  expect(h.pages.map(p => p.text)).toEqual(["old", "new 中文"]);
  h.request(send); expect(send).toHaveBeenCalledTimes(2);
  h.close(); h.request(send, true);
  h.receive(create(TerminalHistoryPageSchema, { requestId: second, lines: ["stale"] }));
  expect(h.pages).toEqual([]); expect(h.pending).not.toBe(""); h.close();
});
it("times out, retries the same cursor, and keeps received data after disconnect", () => {
  vi.useFakeTimers(); const h = new TerminalHistory(vi.fn()); const send = vi.fn();
  h.request(send, true);
  h.receive(create(TerminalHistoryPageSchema, { requestId: h.pending, lines: ["new"], nextCursor: "capture:200" }));
  h.request(send); const timedOut = h.pending;
  vi.advanceTimersByTime(10001); expect(h.error).toContain("超时"); expect(h.pending).toBe("");
  h.request(send); expect(send).toHaveBeenLastCalledWith(h.pending, "capture:200");
  h.receive(create(TerminalHistoryPageSchema, { requestId: timedOut, lines: ["late"] })); expect(h.pages).toHaveLength(1);
  h.receive(create(TerminalHistoryPageSchema, { requestId: h.pending, error: "expired" })); expect(h.error).not.toBe("");
  expect(h.cursor).toBe("capture:200"); h.disconnected(); expect(h.pending).toBe(""); expect(h.pages).toHaveLength(1); h.close();
});

it("shows an empty capture without adding a blank page", () => {
  const h = new TerminalHistory(vi.fn()); h.request(vi.fn(), true);
  h.receive(create(TerminalHistoryPageSchema, { requestId: h.pending }));
  expect(h.pages).toEqual([]); expect(h.cursor).toBe(""); expect(h.error).toBe(""); h.close();
});
