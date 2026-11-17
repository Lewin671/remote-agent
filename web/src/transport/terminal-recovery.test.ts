import { afterEach, expect, test, vi } from "vitest";
import { resetProcessBudgetForTest } from "./recovery";
import { TerminalReconnectController } from "./terminal-recovery";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
  resetProcessBudgetForTest();
});

test("terminal controller reopens after opening failure when main goes online and backs off with global budget", async () => {
  vi.useFakeTimers();
  vi.spyOn(Math, "random").mockReturnValue(0.5);
  let online = true;
  const opens = vi.fn();
  const ctrl = new TerminalReconnectController(() => online, opens);
  try {
    // Opening failure stays closed while online stays true.
    ctrl.onClosed(false);
    expect(opens).not.toHaveBeenCalled();
    // Main transitions offline -> online: reopens immediately using first token.
    online = false;
    online = true;
    ctrl.onMainOnline();
    expect(opens).toHaveBeenCalledTimes(1);
    // Repeated failure before 5s stability enters exponential full-jitter backoff (0.5 * 1000ms = 500ms).
    ctrl.onClosed(true);
    expect(opens).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(499);
    expect(opens).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(opens).toHaveBeenCalledTimes(2);
    // Second consecutive failure doubles window (0.5 * 2000ms = 1000ms).
    ctrl.onClosed(true);
    await vi.advanceTimersByTimeAsync(999);
    expect(opens).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(opens).toHaveBeenCalledTimes(3);
    // Remaining global tokens in processBucket (capacity 4, after 3 used + refills):
    // 5 simultaneous terminals bursting initial reopen are bounded by global 4/2s bucket.
    resetProcessBudgetForTest();
    const burstOpens = vi.fn();
    const peers = Array.from({ length: 5 }, () => new TerminalReconnectController(() => true, burstOpens));
    try {
      for (const p of peers) p.onClosed(true);
      expect(burstOpens).toHaveBeenCalledTimes(4);
      await vi.advanceTimersByTimeAsync(500);
      expect(burstOpens).toHaveBeenCalledTimes(5);
    } finally {
      peers.forEach((p) => p.dispose());
    }
  } finally {
    ctrl.dispose();
  }
});
