import { describe, expect, it } from "vitest";
import { dayGroup, endedTime } from "./model";
import { ALL, resolveScope } from "./scope";

describe("the home's scope (client-v27)", () => {
  it("is the only project when there is one, whatever was saved", () => {
    expect(resolveScope("", ["/a"])).toBe("/a");
    expect(resolveScope(ALL, ["/a"])).toBe("/a");
    expect(resolveScope("/gone", ["/a"])).toBe("/a");
  });

  it("is the saved project while it is listed, else all projects", () => {
    expect(resolveScope("/b", ["/a", "/b"])).toBe("/b");
    expect(resolveScope("/removed", ["/a", "/b"])).toBe(ALL);
    expect(resolveScope("", ["/a", "/b"])).toBe(ALL);
    expect(resolveScope("/a", [])).toBe(ALL);
  });
});

describe("date groups of ended sessions", () => {
  // A Sunday afternoon.
  const now = new Date(2026, 9, 4, 15, 0).getTime();
  const at = (y: number, m: number, d: number, h = 9, min = 5) => new Date(y, m - 1, d, h, min).getTime();

  it("groups by calendar day, then by month", () => {
    expect(dayGroup(at(2026, 10, 4, 0, 1), now)).toBe("今天");
    expect(dayGroup(at(2026, 10, 3, 23, 59), now)).toBe("昨天");
    expect(dayGroup(at(2026, 10, 2), now)).toBe("近 7 天");
    expect(dayGroup(at(2026, 9, 28), now)).toBe("近 7 天");
    expect(dayGroup(at(2026, 9, 27), now)).toBe("9 月");
    expect(dayGroup(at(2025, 12, 31), now)).toBe("2025 年 12 月");
  });

  it("writes the time in the terms of the group", () => {
    expect(endedTime(at(2026, 10, 4), now)).toBe("9:05");
    expect(endedTime(at(2026, 10, 3, 18, 44), now)).toBe("18:44");
    expect(endedTime(at(2026, 9, 29), now)).toBe("周二");
    expect(endedTime(at(2026, 9, 27), now)).toBe("9月27日");
    expect(endedTime(0, now)).toBe("");
  });
});
