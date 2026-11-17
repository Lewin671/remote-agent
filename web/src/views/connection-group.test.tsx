// @vitest-environment happy-dom
// The 「连接」 group on a computer's settings page: a switch, a status row with
// its state graphic, speed rows only where measured, and one sentence under it.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { localeStore, t as message } from "../i18n";
import type { DirectStatus } from "../transport/direct-engine";
import { setDirect } from "../test/direct-state";
import { MachinePage } from "./Settings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLElement;

beforeEach(() => {
  localeStore.overrideForTesting("zh-CN");
  vi.stubGlobal("RTCPeerConnection", class {});
});
const remotes: DemoRemote[] = [];
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  for (const r of remotes.splice(0)) r.dispose();
  localeStore.overrideForTesting(undefined);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

const none = () => undefined;
function open(id = "mac", query = ""): DemoRemote {
  const r = DemoRemote.create(new URLSearchParams(query), async () => undefined);
  remotes.push(r);
  r.start();
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(<MachinePage remote={r} id={id} go={none} onBack={none} />));
  return r;
}
const notify = (r: DemoRemote) => act(() => (r.direct as unknown as { notify(): void }).notify());
const status = (over: Partial<DirectStatus>): DirectStatus => ({ state: "none", file: "relay", read: "relay", fileMiBps: {}, readMs: {}, ...over });
function show(r: DemoRemote, over: Partial<DirectStatus>) {
  vi.spyOn(r.direct, "status").mockReturnValue(status(over));
  notify(r);
}
const group = () => host.querySelector<HTMLElement>(".conn-grp")!;
const sw = () => group().querySelector<HTMLButtonElement>('[role="switch"]')!;
const statusRow = () => group().querySelector<HTMLElement>(".row.dx");
const speedRows = () => [...group().querySelectorAll(".row.speed")].map((e) => [...e.children].map((c) => c.textContent));
const footer = () => host.querySelector(".conn-grp + .gf")!.textContent;

describe("the group's frame", () => {
  it("is titled 连接 and has one switch row first, in place of the old select", () => {
    open();
    const title = group().previousElementSibling!;
    expect([title.className, title.textContent]).toEqual(["label", "连接"]);
    expect(group().firstElementChild!.textContent).toBe(message("transport.direct"));
    expect(group().querySelectorAll('[role="switch"]').length).toBe(1);
    expect(host.querySelector("select")).toBeNull();
  });

  it("explains itself in one short sentence that does not depend on the rows, and names no measuring source", () => {
    const r = open();
    show(r, { state: "none" });
    expect(footer()).toBe(message("transport.footer"));
    expect(footer()).toBe("发消息和审批同时经直连和中继发出，以先到的为准；后台时只用中继。");
    show(r, { state: "connected", file: "direct", fileMiBps: { direct: 6.3 } });
    expect(footer()).toBe("发消息和审批同时经直连和中继发出，以先到的为准；后台时只用中继。");
  });
});

describe("the status row", () => {
  it("draws a green dot and 正在用直连; one purpose or fallback is named on the second line, both write nothing", () => {
    const r = open();
    show(r, { state: "connected", file: "direct", read: "direct" });
    const row = statusRow()!;
    expect(row.querySelector("i.mark")!.className).toContain("online");
    expect(row.querySelector(".dx-text")!.textContent).toBe("正在用直连");
    expect(row.querySelector("small")).toBeNull();
    expect(row.querySelector(".v")).toBeNull();
    show(r, { state: "connected", read: "direct" });
    expect(statusRow()!.querySelector("small")!.textContent).toBe("用于加载");
    show(r, { state: "connected", file: "direct" });
    expect(statusRow()!.querySelector("small")!.textContent).toBe("用于文件");
    show(r, { state: "connected", read: "direct", fileSlow: true });
    expect(statusRow()!.querySelector("small")!.textContent).toBe("文件暂时用中继");
  });

  it("draws a gray dot, never a ring, for 正在用中继 with the slow-fallback reason", () => {
    const r = open();
    show(r, { state: "connected", readSlow: true, fileSlow: true, fileMiBps: { direct: 2, relay: 4 }, readMs: { direct: 220, relay: 40 } });
    const cls = statusRow()!.querySelector("i.mark")!.className;
    expect(cls).toContain("off"); expect(cls).not.toContain("idle");
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toContain("正在用中继");
    expect(statusRow()!.querySelector("small")!.textContent).toBe("直连较慢，暂时改用中继");
  });

  it("draws an ink spinner while setting up, with nothing under it", () => {
    const r = open();
    show(r, { state: "connecting" });
    expect(statusRow()!.querySelector("i.mark")!.className).toContain("run");
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("正在建立直连…");
    expect(statusRow()!.querySelector("small")).toBeNull();
  });

  it("draws a gray dot for 正在用中继 when not established and writes each reason on the second line", () => {
    const r = open();
    show(r, { state: "none" });
    expect(statusRow()!.querySelector("i.mark")!.className).toContain("off");
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("正在用中继正在准备直连");
    expect(statusRow()!.querySelector("small")!.textContent).toBe("正在准备直连");
    const reasons = [
      ["peer_unsupported", "电脑上的版本太旧，更新后可用"],
      ["no_response", "电脑没有回应，可能在电脑上关了直连"],
      ["no_traversal", "两边的网络不能直接连通"],
      ["failed", "没连上，稍后会自动再试"],
      ["at_cap", "同时直连的电脑已达上限"],
      ["security_pending", "安全状态待刷新"],
      ["starting", "正在准备直连"],
    ] as const;
    for (const [reason, text] of reasons) {
      show(r, { state: "none", reason });
      expect(statusRow()!.querySelector("small")!.textContent).toBe(text);
    }
  });

  it("is not drawn for a computer that is offline, and the switch stays", () => {
    const r = open("mini");
    expect(r.peer("mini").reachable).toBe(false);
    show(r, { state: "none", reason: "failed" });
    expect(statusRow()).toBeNull();
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });
});

describe("speed rows", () => {
  it("show only what was measured, with the values in the spec's words", () => {
    const r = open();
    show(r, { state: "connected", file: "direct", fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { direct: 12, relay: 180 } });
    expect(speedRows()).toEqual([["文件传输", "直连 6.3 · 中继 4.1 MiB/s"], ["响应时间", "直连 12 · 中继 180 ms"]]);
    show(r, { state: "connected", file: "direct", fileMiBps: { direct: 6.3 }, readMs: { relay: 180 } });
    expect(speedRows()).toEqual([["文件传输", "直连 6.3 MiB/s"], ["响应时间", "中继 180 ms"]]);
  });

  it("have no response-time row and no placeholder when nothing was measured; files say they are compared next time", () => {
    const r = open();
    show(r, { state: "connected", file: "direct" });
    expect(speedRows()).toEqual([["文件传输", "下次传输时比较"]]);
    expect(group().textContent).not.toMatch(/暂无|没有数据|MiB|ms/);
    show(r, { state: "none", reason: "failed" });
    expect(speedRows()).toEqual([]);
  });

  it("use tabular numbers and wrap their value under the name when narrow", () => {
    const r = open();
    show(r, { state: "connected", file: "direct", fileMiBps: { direct: 6.3 } });
    const row = group().querySelector(".row.speed")!;
    expect(row.children.length).toBe(2);
    expect((row.lastElementChild as HTMLElement).className).toBe("v");
  });
});

describe("the switch", () => {
  it("is a switch for screen readers and the keyboard: role, state, and a name", () => {
    open();
    expect(sw().tagName).toBe("BUTTON");
    expect(sw().getAttribute("aria-label")).toBe("直连");
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });

  it("turns the setting off at once: the status and speed rows close, only this device's setting changes", () => {
    const r = open();
    const set = vi.spyOn(r.direct, "setEnabled");
    show(r, { state: "connected", file: "direct", fileMiBps: { direct: 6.3 } });
    expect(statusRow()).not.toBeNull();
    expect(speedRows().length).toBe(1);
    act(() => sw().click());
    expect(set).toHaveBeenCalledWith("mac", false);
    expect(r.direct.enabled("mac")).toBe(false);
    vi.mocked(r.direct.status).mockRestore();
    notify(r);
    expect(sw().getAttribute("aria-checked")).toBe("false");
    expect(statusRow()).toBeNull();
    expect(speedRows()).toEqual([]);
  });

  it("when turned back on, says 正在建立直连… before the engine reports anything", () => {
    const r = open();
    act(() => sw().click());
    expect(statusRow()).toBeNull();
    act(() => sw().click());
    expect(r.direct.enabled("mac")).toBe(true);
    expect(sw().getAttribute("aria-checked")).toBe("true");
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("正在建立直连…");
    expect(statusRow()!.querySelector("i.mark")!.className).toContain("run");
  });

  it("stops saying trying once a reason or a connection is reported", () => {
    const r = open();
    act(() => sw().click());
    act(() => sw().click());
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("正在建立直连…");
    setDirect(r.direct, "mac", "failed", "no_traversal");
    notify(r);
    expect(statusRow()!.querySelector(".dx-text > .mo-swap")!.textContent).toBe("正在用中继");
    expect(statusRow()!.querySelector("small")!.textContent).toBe("两边的网络不能直接连通");
  });

  it("falls back to 正在用中继 with security/starting reason if nothing at all is reported for a while", () => {
    vi.useFakeTimers();
    try {
      open();
      act(() => sw().click());
      act(() => sw().click());
      expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("正在建立直连…");
      act(() => { vi.advanceTimersByTime(10_001); });
      expect(statusRow()!.querySelector(".dx-text > .mo-swap")!.textContent).toBe("正在用中继");
      expect(statusRow()!.querySelector("small")!.textContent).toBe("安全状态待刷新");
    } finally { vi.useRealTimers(); }
  });

  it("shows a save failure and keeps the switch where it was", () => {
    const r = open();
    vi.spyOn(r.direct, "setEnabled").mockImplementation(() => { throw new Error("存不下"); });
    act(() => sw().click());
    expect(host.querySelector(".banner, [class*=banner]")!.textContent).toContain("存不下");
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });
});

describe("a browser without it", () => {
  it("greys the switch out, says so in the status row, and does nothing when pressed", () => {
    vi.stubGlobal("RTCPeerConnection", undefined);
    const r = open();
    const set = vi.spyOn(r.direct, "setEnabled");
    expect(sw().disabled).toBe(true);
    expect(sw().getAttribute("aria-checked")).toBe("false");
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("这个浏览器不支持直连");
    expect(statusRow()!.querySelector("i.mark")!.className).toContain("off");
    act(() => sw().click());
    expect(set).not.toHaveBeenCalled();
  });
});

describe("in English", () => {
  it("in English says the same things with the same rows", () => {
    localeStore.overrideForTesting("en");
    const r = open();
    show(r, { state: "none", reason: "no_response" });
    expect(group().previousElementSibling!.textContent).toBe("Connection");
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("Using relayThe computer didn't answer; direct may be turned off there");
    show(r, { state: "connected", file: "direct", read: "direct", fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { direct: 12, relay: 180 } });
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("Using direct");
    expect(speedRows()).toEqual([["File transfer", "Direct 6.3 · Relay 4.1 MiB/s"], ["Response time", "Direct 12 · Relay 180 ms"]]);
    expect(footer()).toBe("Messages and approvals are sent over both direct and relay, taking whichever arrives first; only relay is used in the background.");
  });
});

describe("the demo's ?direct= states, which the design review looks at", () => {
  const word = () => statusRow()?.querySelector(".dx-text > .mo-swap")?.textContent;
  const sub = () => statusRow()?.querySelector("small")?.textContent;
  it.each([
    ["direct", "正在用直连", undefined, 2],
    ["file", "正在用直连", "用于文件", 2],
    ["read", "正在用直连", "用于加载", 2],
    ["file-fallback", "正在用直连", "文件暂时用中继", 2],
    ["relay", "正在用中继", "直连较慢，暂时改用中继", 2],
    ["trying", "正在建立直连…", undefined, 0],
    ["none", "正在用中继", "正在准备直连", 0],
    ["peer_unsupported", "直连不可用：电脑上的版本太旧，更新后可用", "电脑上的版本太旧，更新后可用", 0],
    ["no_response", "正在用中继", "电脑没有回应，可能在电脑上关了直连", 0],
    ["no_traversal", "正在用中继", "两边的网络不能直接连通", 0],
    ["failed", "正在用中继", "没连上，稍后会自动再试", 0],
  ])("direct=%s", (name, w, s, rows) => {
    open("mac", `direct=${name}`);
    expect([word(), sub(), speedRows().length]).toEqual([w, s, rows]);
  });

  it("direct=off has the switch off and no rows; turning it on sets up then goes direct", () => {
    vi.useFakeTimers();
    try {
      open("mac", "direct=off");
      expect(sw().getAttribute("aria-checked")).toBe("false");
      expect(statusRow()).toBeNull();
      act(() => sw().click());
      expect(word()).toBe("正在建立直连…");
      act(() => { vi.advanceTimersByTime(2700); });
      expect(word()).toBe("正在用直连");
    } finally { vi.useRealTimers(); }
  });

  it("direct=unsupported greys the switch; speeds=… overrides what was measured", () => {
    open("mac", "direct=unsupported");
    expect(sw().disabled).toBe(true);
    act(() => root?.unmount()); host.remove();
    open("mac", "direct=direct&speeds=relay");
    expect(speedRows()).toEqual([["文件传输", "中继 4.1 MiB/s"], ["响应时间", "中继 217 ms"]]);
    act(() => root?.unmount()); host.remove();
    open("mac", "direct=direct&speeds=none");
    expect(speedRows()).toEqual([["文件传输", "下次传输时比较"]]);
  });
});
