import { expect, it } from "vitest";
import { Attempt } from "./attempt";
import { CommandError } from "./remote";

it("repeats an attempt with an unknown outcome exactly", () => {
  const a = new Attempt<{ model: string }>();
  const first = a.next({ model: "" });
  a.settle(new CommandError("unknown", "连接超时"));
  expect(a.frozen).toBe(true);
  // The created session's metadata arrived meanwhile and changed the
  // configuration; the retry must still send the first request.
  const retry = a.next({ model: "gpt-6-astra" });
  expect(retry.payload).toEqual({ model: "" });
  expect(retry.slot).toBe(first.slot);
});

it("starts afresh after success or a definite answer", () => {
  const a = new Attempt<string>();
  const first = a.next("x");
  a.settle(new CommandError("failed_precondition", "cwd is not a directory"));
  expect(a.frozen).toBe(false);
  const second = a.next("y");
  expect(second.payload).toBe("y");
  expect(second.slot.requestId).not.toBe(first.slot.requestId);
  a.settle();
  expect(a.next("z").payload).toBe("z");
});
