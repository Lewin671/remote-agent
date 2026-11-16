// The direct path in words: one table, used by the computer's page and the connection check.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { localeStore, t as message } from "../i18n";
import type { DirectStatus } from "../transport/direct-engine";
import { directView, relayNotSlower } from "./direct-status";

const base: DirectStatus = { state: "none", file: "relay", read: "relay", fileMiBps: {}, readMs: {} };
const make = (over: Partial<DirectStatus>): DirectStatus => ({ ...base, ...over });

beforeEach(() => localeStore.overrideForTesting("zh-CN"));
afterEach(() => localeStore.overrideForTesting(undefined));

describe("directView", () => {
  it("is green and says 正在直连 with the purposes that go direct", () => {
    const both = directView(make({ state: "connected", file: "direct", read: "direct" }));
    expect([both.kind, both.mark, both.word, both.value, both.sub, both.summary]).toEqual(["direct", "online", "正在直连", "文件和读取", undefined, "正在直连 · 文件和读取"]);
    expect(directView(make({ state: "connected", file: "direct" })).value).toBe("文件");
    expect(directView(make({ state: "connected", read: "direct" })).value).toBe("读取");
    expect(directView(make({ state: "connected", file: "direct" })).summary).toBe("正在直连 · 文件");
  });

  it("is a hollow ring while both purposes still use the relay, and says why", () => {
    const v = directView(make({ state: "connected" }));
    expect([v.kind, v.mark, v.word, v.value, v.summary]).toEqual(["relay", "idle", "已连上，暂用中继", undefined, "已连上，暂用中继"]);
    expect(v.sub).toBe("正在比较速度");
    // Measured on both paths and the relay is not slower: the relay is faster.
    const faster = directView(make({ state: "connected", fileMiBps: { direct: 3, relay: 4 }, readMs: { direct: 200, relay: 180 } }));
    expect(faster.sub).toBe("中继更快");
    // Equal counts as not slower.
    expect(directView(make({ state: "connected", fileMiBps: { direct: 4, relay: 4 } })).sub).toBe("中继更快");
  });

  it("keeps comparing while direct is ahead or only one path was measured", () => {
    for (const over of [
      { fileMiBps: { direct: 6, relay: 4 } },
      { readMs: { direct: 12, relay: 180 } },
      { fileMiBps: { relay: 9 } },
      { fileMiBps: { direct: 3, relay: 4 }, readMs: { direct: 12, relay: 180 } },
      {},
    ] satisfies Partial<DirectStatus>[])
      expect(directView(make({ state: "connected", ...over })).sub).toBe("正在比较速度");
  });

  it("spins while trying", () => {
    const v = directView(make({ state: "connecting" }));
    expect([v.kind, v.mark, v.word, v.sub, v.summary]).toEqual(["trying", "run", "正在尝试直连…", undefined, "正在尝试直连…"]);
  });

  it("is a faint dot when not direct, with the reason as the second line", () => {
    const none = directView(make({ state: "none" }));
    expect([none.kind, none.mark, none.word, none.sub, none.summary]).toEqual(["none", "off", "未直连", undefined, "未直连"]);
    const reasons = {
      peer_unsupported: "电脑上的版本太旧，更新后可用",
      no_response: "电脑没有回应，可能在电脑上关了直连",
      no_traversal: "两边的网络不能直接连通",
      failed: "没连上，稍后会自动再试",
    } as const;
    for (const [reason, text] of Object.entries(reasons)) {
      const v = directView(make({ state: "none", reason: reason as keyof typeof reasons }));
      expect([v.sub, v.summary]).toEqual([text, `未直连 · ${text}`]);
    }
  });

  it("speaks English with the same shape", () => {
    localeStore.overrideForTesting("en");
    const v = directView(make({ state: "none", reason: "no_traversal", fileMiBps: { relay: 4.1 } }));
    expect(v.summary).toBe("Not direct · The two networks can't reach each other directly");
    expect(v.speeds[0].value).toBe("Relay 4.1 MiB/s");
    expect(directView(make({ state: "connected", file: "direct" })).summary).toBe("Connected directly · Files");
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
      ["读取延时", "直连 12 · 中继 180 ms"],
    ]);
  });

  it("shows the one path that was measured, and rounds large rates", () => {
    expect(speeds({ fileMiBps: { direct: 6.34 } })).toEqual([["文件传输", "直连 6.3 MiB/s"]]);
    expect(speeds({ fileMiBps: { relay: 4.06 } })).toEqual([["文件传输", "中继 4.1 MiB/s"]]);
    expect(speeds({ fileMiBps: { direct: 123.4, relay: 99.96 } })).toEqual([["文件传输", "直连 123 · 中继 100 MiB/s"]]);
    expect(speeds({ readMs: { direct: 12 } })).toEqual([["读取延时", "直连 12 ms"]]);
    expect(speeds({ readMs: { relay: 180 } })).toEqual([["读取延时", "中继 180 ms"]]);
  });

  it("has no rows, and no placeholder, when nothing was measured", () => {
    expect(speeds({})).toEqual([]);
    expect(speeds({ state: "none", reason: "failed" })).toEqual([]);
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
