import { describe, expect, it } from "vitest";
import { shiftedWeights } from "./theme";

describe("shiftedWeights", () => {
  it("keeps the mockup's weights on Apple devices", () => {
    expect(shiftedWeights("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/140.0 Safari/537.36")).toBe(false);
    expect(shiftedWeights("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148")).toBe(false);
  });
  it("shifts them where Chinese comes from Noto or YaHei", () => {
    expect(shiftedWeights("Mozilla/5.0 (Linux; Android 16; Pixel 9) AppleWebKit/537.36 Chrome/140.0 Mobile Safari/537.36")).toBe(true);
    expect(shiftedWeights("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0 Safari/537.36")).toBe(true);
  });
});
