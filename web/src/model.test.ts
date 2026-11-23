import { describe, expect, it } from "vitest";
import { futureTime, shortenPath } from "./model";

describe("whole paths (file edit titles)", () => {
  const cases: [string, string, string, string][] = [
    ["/Users/me/Code/foo bar/a.go", "/Users/me/Code/foo", "/Users/me", "~/Code/foo bar/a.go"],
    ["/Users/me/Code/foo/sub dir/b.go", "/Users/me/Code/foo", "/Users/me", "sub dir/b.go"],
    ["/private/tmp/p/x y.txt", "/tmp/p", "/Users/me", "x y.txt"],
    ["/etc/x y", "/Users/me/Code/foo", "/Users/me", "/etc/x y"],
  ];
  it.each(cases)("shows %s in %s as expected", (path, cwd, home, shown) => {
    expect(shortenPath(path, cwd, home)).toBe(shown);
  });
});

describe("future times", () => {
  const now = new Date(2026, 9, 20, 23, 55).getTime();
  it.each([
    [new Date(2026, 9, 20, 3, 59, 1).getTime(), "今天 03:59"],
    [new Date(2026, 9, 21, 0, 2, 59).getTime(), "明天 00:02"],
    [new Date(2026, 9, 22, 3, 59, 1).getTime(), "10月22日 03:59"],
    [new Date(2026, 9, 22, 23, 59, 59).getTime(), "10月22日 23:59"],
  ])("writes %s as %s without locale-dependent seconds or AM/PM", (time, expected) => {
    expect(futureTime(time, now)).toBe(expected);
    expect(futureTime(BigInt(time), now)).toBe(expected);
  });
  it.each([
    [new Date(2026, 0, 31, 23, 59).getTime(), new Date(2026, 1, 1, 3, 59).getTime()],
    [new Date(2026, 11, 31, 23, 59).getTime(), new Date(2027, 0, 1, 3, 59).getTime()],
    [new Date(2026, 2, 7, 23, 59).getTime(), new Date(2026, 2, 8, 3, 59).getTime()],
  ])("recognizes tomorrow across calendar boundaries", (current, time) => {
    expect(futureTime(time, current)).toBe("明天 03:59");
  });
  it("formats pairing expiry as a 24-hour clock only, including after midnight", () => {
    expect(futureTime(new Date(2026, 9, 21, 5, 2, 59).getTime(), now, "time")).toBe("05:02");
    expect(futureTime(new Date(2026, 9, 21, 0, 0, 1).getTime(), now, "time")).toBe("00:00");
  });
});
