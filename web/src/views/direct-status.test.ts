// The direct path in words: one table, used by the computer's page and the connection check.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { localeStore, t as message } from "../i18n";
import type { DirectStatus } from "../transport/direct-engine";
import { directView } from "./direct-status";

const base: DirectStatus = { state: "none", file: "relay", read: "relay", fileMiBps: {}, readMs: {} };
const make = (over: Partial<DirectStatus>): DirectStatus => ({ ...base, ...over });

beforeEach(() => localeStore.overrideForTesting("zh-CN"));
afterEach(() => localeStore.overrideForTesting(undefined));

describe("directView", () => {
  it("is a green dot and says 正在用直连; one purpose says which or why the other fell back, both write nothing", () => {
    const both = directView(make({ state: "connected", file: "direct", read: "direct" }));
    expect([both.kind, both.mark, both.word, both.sub, both.summary]).toEqual(["direct", "online", "正在用直连", undefined, "正在用直连"]);

    const file = directView(make({ state: "connected", file: "direct" }));
    expect([file.sub, file.summary]).toEqual(["用于文件", "正在用直连 · 用于文件"]);

    const read = directView(make({ state: "connected", read: "direct" }));
    expect([read.sub, read.summary]).toEqual(["用于加载", "正在用直连 · 用于加载"]);

    expect(directView(make({ state: "connected", read: "direct", fileSlow: true })).sub).toBe("文件暂时用中继");
    expect(directView(make({ state: "connected", file: "direct", readSlow: true })).sub).toBe("加载暂时用中继");
  });

  it("is a gray dot saying 正在用中继 and 直连较慢，暂时改用中继 when both fell back or neither is on direct", () => {
    const slowBoth = directView(make({ state: "connected", readSlow: true, fileSlow: true }));
    expect([slowBoth.kind, slowBoth.mark, slowBoth.word, slowBoth.sub, slowBoth.summary]).toEqual([
      "relay", "off", "正在用中继", "直连较慢，暂时改用中继", "正在用中继 · 直连较慢，暂时改用中继",
    ]);

    const onlyOneFallback = directView(make({ state: "connected", fileSlow: true }));
    expect([onlyOneFallback.kind, onlyOneFallback.sub]).toEqual(["relay", "直连较慢，暂时改用中继"]);

    const noFallback = directView(make({ state: "connected" }));
    expect([noFallback.kind, noFallback.mark, noFallback.word, noFallback.sub, noFallback.summary]).toEqual([
      "relay", "off", "正在用中继", "直连通道尚未就绪", "正在用中继 · 直连通道尚未就绪",
    ]);

    // Not connected never shows slow-fallback wording.
    for (const over of [{ state: "connecting" }, { state: "none" }, { state: "none", reason: "failed" }] as const) {
      const w = directView(make({ ...over, readSlow: true, fileSlow: true, readMs: { direct: 13, relay: 335 } }));
      expect(w.summary ?? "").not.toContain("直连较慢");
    }
  });

  it("spins while setting up", () => {
    const v = directView(make({ state: "connecting" }));
    expect([v.kind, v.mark, v.word, v.sub, v.summary]).toEqual(["trying", "run", "正在建立直连…", undefined, "正在建立直连…"]);
  });

  it("is a gray dot and 正在用中继 when not established, with the reason as the second line", () => {
    const none = directView(make({ state: "none" }));
    expect([none.kind, none.mark, none.word, none.sub, none.summary]).toEqual(["relay", "off", "正在用中继", "正在准备直连", "正在用中继 · 正在准备直连"]);
    const reasons = {
      peer_unsupported: "电脑上的版本太旧，更新后可用",
      no_response: "电脑没有回应，可能在电脑上关了直连",
      no_traversal: "两边的网络不能直接连通",
      failed: "没连上，稍后会自动再试",
      at_cap: "同时直连的电脑已达上限",
      security_pending: "安全状态待刷新",
      starting: "正在准备直连",
    } as const;
    for (const [reason, text] of Object.entries(reasons)) {
      const v = directView(make({ state: "none", reason: reason as keyof typeof reasons }));
      const expectedSub = reason === "peer_unsupported" ? `直连不可用：${text}` : text;
      expect([v.sub, v.summary]).toEqual([
        reason === "peer_unsupported" ? text : expectedSub,
        `正在用中继 · ${expectedSub}`,
      ]);
    }
  });

  it("never draws the hollow ring: no state uses the idle mark", () => {
    const states: Partial<DirectStatus>[] = [
      { state: "connected", file: "direct", read: "direct" },
      { state: "connected", readSlow: true, fileSlow: true },
      { state: "connected", file: "direct" },
      { state: "connecting" },
      { state: "none" },
      { state: "none", reason: "failed" },
      { state: "unsupported" },
    ];
    for (const over of states) expect(directView(make(over)).mark).not.toBe("idle");
  });

  it("speaks English with the same shape", () => {
    localeStore.overrideForTesting("en");
    const v = directView(make({ state: "none", reason: "no_traversal", fileMiBps: { relay: 4.1 } }));
    expect(v.summary).toBe("Using relay · The two networks can't reach each other directly");
    expect(v.speeds[0].value).toBe("Relay 4.1 MiB/s");
    expect(directView(make({ state: "connected", file: "direct" })).summary).toBe("Using direct · Used for files");
    expect(directView(make({ state: "connected", read: "direct", fileSlow: true })).sub).toBe("Files using relay for now");
    expect(directView(make({ state: "connected", readSlow: true, fileSlow: true })).sub).toBe("Direct is slow; using relay for now");
    expect(directView(make({ state: "connecting" })).word).toBe("Setting up direct…");
  });

  it("calls a switched-off path off in the check and reports relay with empty detail", () => {
    const v = directView(make({ state: "none", reason: "disabled" }));
    expect([v.kind, v.word, v.summary]).toEqual(["off", "正在用中继", "直连已关闭"]);
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

  it("comes from the same message source as everything else", () => {
    expect(message("transport.file_speed_both", { direct: "1", relay: "2" })).toBe("直连 1 · 中继 2 MiB/s");
  });
});
