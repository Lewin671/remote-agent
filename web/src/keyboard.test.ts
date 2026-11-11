// @vitest-environment happy-dom
import { expect, it, vi } from "vitest";
import { afterKeyboardHidden } from "./keyboard";

it("opens only after the keyboard has closed, and cancels a pending opening", () => {
  vi.useFakeTimers();
  const viewport = Object.assign(new EventTarget(), { height: 400, scale: 1 });
  vi.stubGlobal("visualViewport", viewport);
  vi.stubGlobal("innerHeight", 400);
  const input = document.createElement("textarea");
  document.body.append(input);
  input.focus();
  const open = vi.fn();
  const cancel = afterKeyboardHidden(open, 700);
  try {
    expect(document.activeElement).not.toBe(input);
    vi.runAllTimers();
    expect(open).not.toHaveBeenCalled();
    viewport.height = 550;
    viewport.dispatchEvent(new Event("resize"));
    vi.runAllTimers();
    expect(open).not.toHaveBeenCalled();
    viewport.height = 700;
    viewport.dispatchEvent(new Event("resize"));
    vi.runAllTimers();
    expect(open).toHaveBeenCalledTimes(1);
    viewport.height = 400;
    const stop = afterKeyboardHidden(open, 700);
    stop();
    viewport.height = 700;
    viewport.dispatchEvent(new Event("resize"));
    vi.runAllTimers();
    expect(open).toHaveBeenCalledTimes(1);
  } finally {
    cancel();
    input.remove();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  }
});
