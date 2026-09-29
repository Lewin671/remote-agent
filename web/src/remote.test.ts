import { Code, ConnectError } from "@connectrpc/connect";
import { expect, it } from "vitest";
import { CommandError, COMMAND_TTL_MS, newSlot, uncertain } from "./remote";

it("reuses a request slot only when the outcome is unknown", () => {
  // Timed out or cut off: the command may have run; a retry must reuse
  // the slot so the daemon answers from its log.
  expect(uncertain(new CommandError("unknown", "连接超时"))).toBe(true);
  expect(uncertain(new CommandError("aborted", "outcome unknown"))).toBe(true);
  expect(uncertain(new ConnectError("reset", Code.Internal))).toBe(true);
  // A definite answer: the next attempt starts afresh.
  expect(uncertain(new CommandError("unavailable", "电脑不在线"))).toBe(false);
  expect(uncertain(new CommandError("failed_precondition", "cwd is not a directory"))).toBe(false);
  expect(uncertain(new Error("意外的回复"))).toBe(false);
});

it("gives each slot a fresh ID and a deadline a minute out", () => {
  const a = newSlot(1000);
  const b = newSlot(1000);
  expect(a.requestId).not.toBe(b.requestId);
  expect(a.requestId).toMatch(/^[0-9a-f]{32}$/);
  expect(a.deadline).toBe(1000 + COMMAND_TTL_MS);
});
