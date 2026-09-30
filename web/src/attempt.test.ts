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

it("keeps an unknown start unknown when its retry comes back expired", () => {
  const a = new Attempt<string>();
  const first = a.next("x");
  a.settle(new CommandError("unknown", "连接超时"));
  const retry = a.next("y");
  expect(retry.slot).toBe(first.slot);
  // The daemon forgot it: that says nothing about whether it ran.
  a.settle(new CommandError("deadline_exceeded", "the command expired before it reached the machine"));
  expect(a.frozen).toBe(true);
  expect(a.expired).toBe(true);
  // Nothing new replaces it until it is given up.
  expect(a.next("z").slot).toBe(first.slot);
  a.abandon();
  expect(a.frozen).toBe(false);
  expect(a.next("z").slot.requestId).not.toBe(first.slot.requestId);
});

it("takes a first send refused as expired as not run", () => {
  const a = new Attempt<string>();
  a.next("x");
  a.settle(new CommandError("deadline_exceeded", "check the device clocks"));
  expect(a.frozen).toBe(false);
});
