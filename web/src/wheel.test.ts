import { expect, it } from "vitest";
import { wheelAxis } from "./wheel";

it("keeps a scroll's first axis until it pauses", () => {
  const axis = wheelAxis(200);
  expect(axis({ deltaX: 30, deltaY: -2, timeStamp: 0 })).toBe("x");
  // Drifting up mid-pan stays a pan.
  expect(axis({ deltaX: 1, deltaY: -20, timeStamp: 16 })).toBe("x");
  expect(axis({ deltaX: 0, deltaY: -40, timeStamp: 150 })).toBe("x");
  // After a pause a new scroll decides afresh.
  expect(axis({ deltaX: 0, deltaY: -40, timeStamp: 400 })).toBe("y");
});

it("waits for an event that moves", () => {
  const axis = wheelAxis();
  expect(axis({ deltaX: 0, deltaY: 0, timeStamp: 0 })).toBe("");
  expect(axis({ deltaX: 0, deltaY: 5, timeStamp: 10 })).toBe("y");
});
