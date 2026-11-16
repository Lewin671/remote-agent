// The direct path in words: one table, used by the computer's page and the connection check.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { localeStore, t as message } from "../i18n";
import type { DirectStatus } from "../transport/direct-engine";
import { directView, relayNotSlower } from "./direct-status";

const base: DirectStatus = { state: "none", file: "relay", read: "relay", fileMiBps: {}, readMs: {}, warming: false };
const make = (over: Partial<DirectStatus>): DirectStatus => ({ ...base, ...over });

beforeEach(() => localeStore.overrideForTesting("zh-CN"));
afterEach(() => localeStore.overrideForTesting(undefined));

describe("directView", () => {
  it("is a green dot and says 正在用直连; one purpose says which, both say nothing", () => {
    const both = directView(make({ state: "connected", file: "direct", read: "direct" }));
    expect([both.kind, both.mark, both.word, both.sub, both.summary]).toEqual(["direct", "online", "正在用直连", undefined, "正在用直连"]);
    const file = directView(make({ state: "connected", file: "direct" }));
    expect([file.sub, file.summary]).toEqual(["用于文件", "正在用直连 · 用于文件"]);
    expect(directView(make({ state: "connected", read: "direct" })).sub).toBe("用于加载");
    // Direct in use wins over a warm-up that is still running.
    expect(directView(make({ state: "connected", read: "direct", warming: true })).kind).toBe("direct");
  });

  it("spins while connected and measuring, in the first seconds", () => {
    const v = directView(make({ state: "connected", warming: true }));
    expect([v.kind, v.mark, v.word, v.sub, v.summary]).toEqual(["measuring", "run", "直连已连上，正在测速", undefined, "直连已连上，正在测速"]);
  });

  it("is a gray dot saying 正在用中继 once measured and not worth switching, and says which of the two reasons", () => {
    const v = directView(make({ state: "connected" }));
    expect([v.kind, v.mark, v.word, v.summary]).toEqual(["relay", "off", "正在用中继", "正在用中继 · 直连没有明显更快"]);
    expect(v.sub).toBe("直连没有明显更快");
    // Measured on both paths and the relay is not slower: the relay is faster.
    const faster = directView(make({ state: "connected", fileMiBps: { direct: 3, relay: 4 }, readMs: { direct: 200, relay: 180 } }));
    expect(faster.sub).toBe("中继更快");
    // Equal counts as not slower.
    expect(directView(make({ state: "connected", fileMiBps: { direct: 4, relay: 4 } })).sub).toBe("中继更快");
  });

  it("says no clear win while direct is ahead only slightly, or only one path was measured", () => {
    for (const over of [
      { fileMiBps: { direct: 6, relay: 4 } },
      { readMs: { direct: 160, relay: 175 } },
      { fileMiBps: { relay: 9 } },
      { fileMiBps: { direct: 3, relay: 4 }, readMs: { direct: 12, relay: 180 } },
      {},
    ] satisfies Partial<DirectStatus>[])
      expect(directView(make({ state: "connected", ...over })).sub).toBe("直连没有明显更快");
  });

  it("spins while setting up", () => {
    const v = directView(make({ state: "connecting" }));
    expect([v.kind, v.mark, v.word, v.sub, v.summary]).toEqual(["trying", "run", "正在建立直连…", undefined, "正在建立直连…"]);
  });

  it("is a gray dot and 正在用中继 when not established, with the reason as the second line", () => {
    const none = directView(make({ state: "none" }));
    expect([none.kind, none.mark, none.word, none.sub, none.summary]).toEqual(["relay", "off", "正在用中继", undefined, "正在用中继"]);
    const reasons = {
      peer_unsupported: "电脑上的版本太旧，更新后可用",
      no_response: "电脑没有回应，可能在电脑上关了直连",
      no_traversal: "两边的网络不能直接连通",
      failed: "没连上，稍后会自动再试",
    } as const;
    for (const [reason, text] of Object.entries(reasons)) {
      const v = directView(make({ state: "none", reason: reason as keyof typeof reasons }));
      expect([v.sub, v.summary]).toEqual([`直连不可用：${text}`, `正在用中继 · 直连不可用：${text}`]);
    }
  });

  it("never draws the hollow ring: no state uses the idle mark", () => {
    const states: Partial<DirectStatus>[] = [{ state: "connected" }, { state: "connected", warming: true }, { state: "connected", file: "direct" }, { state: "connecting" }, { state: "none" }, { state: "none", reason: "failed" }, { state: "unsupported" }];
    for (const over of states) expect(directView(make(over)).mark).not.toBe("idle");
  });

  it("speaks English with the same shape", () => {
    localeStore.overrideForTesting("en");
    const v = directView(make({ state: "none", reason: "no_traversal", fileMiBps: { relay: 4.1 } }));
    expect(v.summary).toBe("Using relay · Direct unavailable: The two networks can't reach each other directly");
    expect(v.speeds[0].value).toBe("Relay 4.1 MiB/s");
    expect(directView(make({ state: "connected", file: "direct" })).summary).toBe("Using direct · Used for files");
    expect(directView(make({ state: "connected", warming: true })).word).toBe("Direct connected, measuring speed");
    expect(directView(make({ state: "connected" })).sub).toBe("Direct isn't clearly faster");
    expect(directView(make({ state: "connecting" })).word).toBe("Setting up direct…");
  });

  it("calls a switched-off path off in the check and draws no status row", () => {
    const v = directView(make({ state: "none", reason: "disabled" }));
    expect([v.kind, v.word, v.summary]).toEqual(["off", "", "直连已关闭"]);
  });

  it("names a browser without it, and leaves the check no line of its own", () => {
    const v = directView(make({ state: "unsupported" }));
    expect([v.kind, v.word, v.summary]).toEqual(["unsupported", "这个浏览器不支持直连", undefined]);
  });
});

describe("speeds", () => {
  const speeds = (over: Partial<DirectStatus>) => directView(make({ state: "connected", ...over })).speeds.map((s) => [s.name, s.value]);

  it("shows both paths in one value, one row per purpose", () => {
    expect(speeds({ fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { direct: 12.4, relay: 180 } })).toEqual([
      ["文件传输", "直连 6.3 · 中继 4.1 MiB/s"],
      ["响应时间", "直连 12 · 中继 180 ms"],
    ]);
  });

  it("shows the one path that was measured, and rounds large rates", () => {
    expect(speeds({ fileMiBps: { direct: 6.34 } })).toEqual([["文件传输", "直连 6.3 MiB/s"]]);
    expect(speeds({ fileMiBps: { relay: 4.06 } })).toEqual([["文件传输", "中继 4.1 MiB/s"]]);
    expect(speeds({ fileMiBps: { direct: 123.4, relay: 99.96 } })).toEqual([["文件传输", "直连 123 · 中继 100 MiB/s"]]);
    expect(speeds({ fileMiBps: { direct: 1 }, readMs: { direct: 12 } }).slice(1)).toEqual([["响应时间", "直连 12 ms"]]);
    expect(speeds({ fileMiBps: { direct: 1 }, readMs: { relay: 180 } }).slice(1)).toEqual([["响应时间", "中继 180 ms"]]);
  });

  it("has no response-time row, and no placeholder, when nothing was measured", () => {
    expect(speeds({ state: "none", reason: "failed" })).toEqual([]);
    expect(speeds({ readMs: {} }).map((r) => r[0])).not.toContain("响应时间");
  });

  it("says honestly that files are compared on the next transfer, only when connected and not measured", () => {
    expect(speeds({})).toEqual([["文件传输", "下次传输时比较"]]);
    expect(speeds({ fileMiBps: { relay: 4.1 } })).toEqual([["文件传输", "中继 4.1 MiB/s"]]);
    expect(directView(make({ state: "connecting" })).speeds).toEqual([]);
    localeStore.overrideForTesting("en");
    expect(speeds({})).toEqual([["File transfer", "Compared on the next transfer"]]);
    expect(speeds({ readMs: { direct: 48, relay: 175 } })).toContainEqual(["Response time", "Direct 48 · Relay 175 ms"]);
  });

  it("comes from the same message source as everything else", () => {
    expect(message("transport.file_speed_both", { direct: "1", relay: "2" })).toBe("直连 1 · 中继 2 MiB/s");
  });
});

describe("relayNotSlower", () => {
  it("needs a purpose measured on both paths", () => {
    expect(relayNotSlower({ fileMiBps: {}, readMs: {} })).toBe(false);
    expect(relayNotSlower({ fileMiBps: { relay: 4 }, readMs: { direct: 1 } })).toBe(false);
    expect(relayNotSlower({ fileMiBps: { direct: 4, relay: 4 }, readMs: {} })).toBe(true);
    expect(relayNotSlower({ fileMiBps: {}, readMs: { direct: 200, relay: 180 } })).toBe(true);
    expect(relayNotSlower({ fileMiBps: { direct: 5, relay: 4 }, readMs: { direct: 200, relay: 180 } })).toBe(false);
  });
});
