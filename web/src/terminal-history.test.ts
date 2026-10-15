import { afterEach, expect, it, vi } from "vitest";
import { TerminalHistory } from "./terminal-history";
import { create } from "@bufbuild/protobuf";
import { TerminalHistoryPageSchema } from "./gen/remoteagent/v1/remote_pb";
afterEach(() => vi.useRealTimers());

it("stays live until the first page arrives, then prepends immutable pages and ignores abandoned replies", () => {
  const h = new TerminalHistory(vi.fn()); const send = vi.fn();
  h.request(send, true); const first = h.pending;
  expect(h.phase).toBe("loading"); expect(h.visible).toBe(false);
  h.request(send); expect(send).toHaveBeenCalledTimes(1);
  h.receive(create(TerminalHistoryPageSchema, { requestId: first, lines: ["new 中文"], nextCursor: "capture:200" }));
  expect(h.phase).toBe("reading"); expect(h.visible).toBe(true);
  h.request(send); const second = h.pending;
  expect(send).toHaveBeenLastCalledWith(second, "capture:200");
  h.receive(create(TerminalHistoryPageSchema, { requestId: second, lines: ["old"], nextCursor: "" }));
  expect(h.pages.map(p => p.lines)).toEqual([["old"], ["new 中文"]]);
  h.request(send); expect(send).toHaveBeenCalledTimes(2);
  h.close(); expect(h.phase).toBe("idle"); h.request(send, true);
  h.receive(create(TerminalHistoryPageSchema, { requestId: second, lines: ["stale"] }));
  expect(h.pages).toEqual([]); expect(h.pending).not.toBe(""); h.close();
});

it("times out as a retryable failure, reuses the cursor, keeps data after disconnect and marks an expired capture", () => {
  vi.useFakeTimers(); const h = new TerminalHistory(vi.fn()); const send = vi.fn();
  h.request(send, true);
  h.receive(create(TerminalHistoryPageSchema, { requestId: h.pending, lines: ["new"], nextCursor: "capture:200" }));
  h.request(send); const timedOut = h.pending;
  vi.advanceTimersByTime(10001); expect(h.error).toBe("failed"); expect(h.pending).toBe("");
  h.request(send); expect(send).toHaveBeenLastCalledWith(h.pending, "capture:200");
  h.receive(create(TerminalHistoryPageSchema, { requestId: timedOut, lines: ["late"] })); expect(h.pages).toHaveLength(1);
  h.receive(create(TerminalHistoryPageSchema, { requestId: h.pending, error: "history expired; reopen history" }));
  expect(h.error).toBe("expired");
  expect(h.cursor).toBe("capture:200"); h.disconnected(); expect(h.closed).toBe(true); expect(h.pending).toBe(""); expect(h.pages).toHaveLength(1); h.close();
});

it("does not open the reader for an empty capture", () => {
  const h = new TerminalHistory(vi.fn()); h.request(vi.fn(), true);
  h.receive(create(TerminalHistoryPageSchema, { requestId: h.pending }));
  expect(h.phase).toBe("idle"); expect(h.pages).toEqual([]); expect(h.cursor).toBe(""); expect(h.error).toBe(""); h.close();
});

it("keeps the live screen up and says so when the first capture fails, and a new pull retries", () => {
  const h = new TerminalHistory(vi.fn()); h.request(vi.fn(), true);
  h.receive(create(TerminalHistoryPageSchema, { requestId: h.pending, error: "history expired; reopen history" }));
  expect(h.phase).toBe("loading"); expect(h.error).toBe("failed"); expect(h.pages).toEqual([]);
  const send = vi.fn(); h.request(send, true);
  expect(send).toHaveBeenCalledWith(h.pending, ""); expect(h.error).toBe("");
  h.receive(create(TerminalHistoryPageSchema, { requestId: h.pending, lines: ["a"] }));
  expect(h.phase).toBe("reading"); h.close();
});

it("says a timed-out first capture failed without opening the reader", () => {
  vi.useFakeTimers(); const h = new TerminalHistory(vi.fn());
  h.request(vi.fn(), true);
  vi.advanceTimersByTime(10001);
  expect(h.phase).toBe("loading"); expect(h.error).toBe("failed"); expect(h.pending).toBe("");
  h.disconnected(); expect(h.phase).toBe("idle"); h.close();
});
