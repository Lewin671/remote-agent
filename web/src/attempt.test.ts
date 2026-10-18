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
  a.settle(
    new CommandError(
      "deadline_exceeded",
      "the command expired before it reached the machine",
    ),
  );
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

it("keeps an unknown start unknown when its retry finds the machine offline", () => {
  const a = new Attempt<string>();
  const first = a.next("x");
  a.settle(new CommandError("unknown", "连接超时"));
  a.next("x");
  a.settle(new CommandError("unavailable", "电脑不在线，指令没有发出"));
  expect(a.frozen).toBe(true);
  expect(a.next("y").slot).toBe(first.slot);
});

it("is settled only by the request that came back", () => {
  const a = new Attempt<string>();
  const x = a.next("x");
  // x is given up while it is on its way, and y is sent.
  a.abandon();
  const y = a.next("y");
  expect(y.slot.requestId).not.toBe(x.slot.requestId);
  // x's answer says nothing about y.
  a.settle(undefined, x);
  expect(a.payload).toBe("y");
  a.settle(new CommandError("unknown", "连接超时"), y);
  expect(a.frozen).toBe(true);
  expect(a.next("z").slot).toBe(y.slot);
  // Nor does a failure of x free y.
  a.settle(new CommandError("failed_precondition", "no"), x);
  expect(a.next("z").slot).toBe(y.slot);
});

it("never replaces an unknown intent after security signing fails locally", () => {
  const a = new Attempt<string>();
  const first = a.next("x");
  a.settle(new CommandError("unknown", "relay withheld result"));
  a.next("x");
  a.settle(new CommandError("security_unavailable", "security state unavailable"));
  expect(a.frozen).toBe(true);
  expect(a.next("y").slot).toBe(first.slot);
  a.settle(
    new CommandError(
      "failed_precondition",
      "authenticated executor refusal",
      true,
    ),
  );
  expect(a.frozen).toBe(false);
});
