import { beforeEach } from "vitest";
import { Recovery } from "../transport/recovery";

// Each test is a separate application scenario. Within a test all Remotes
// still share the real process budget, including foreground restarts. Do not
// replace Recovery or its global-budget selection with a mock implementation.
beforeEach(() => {
  const budget = (new Recovery() as unknown as { global: { tokens: number; capacity: number; at?: number } }).global;
  budget.tokens = budget.capacity;
  budget.at = undefined;
});
