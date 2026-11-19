// @vitest-environment happy-dom
// Module 3 「直连」 summary row + unified DirectSettingsSheet (switch + status + inline diagnostics).

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
function open(id = "mac", query = "", section?: string): DemoRemote {
  const r = DemoRemote.create(new URLSearchParams(query), async () => undefined);
  remotes.push(r);
  r.start();
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(<MachinePage remote={r} id={id} section={section} go={none} onBack={none} />));
  return r;
}
const notify = (r: DemoRemote) => act(() => (r.direct as unknown as { notify(): void }).notify());
const status = (over: Partial<DirectStatus>): DirectStatus => ({ state: "none", file: "relay", read: "relay", fileMiBps: {}, readMs: {}, ...over });
function show(r: DemoRemote, over: Partial<DirectStatus>) {
  vi.spyOn(r.direct, "status").mockReturnValue(status(over));
  notify(r);
}
const summaryRow = () =>
  [...host.querySelectorAll<HTMLButtonElement>("button.row")].find(
    (b) => b.querySelector(".row-name b")?.textContent === message("transport.direct"),
  )!;
const openSheet = (id = "mac", query = ""): DemoRemote => {
  const r = open(id, query);
  act(() => summaryRow().click());
  return r;
};
const sheet = () => document.querySelector<HTMLElement>(".sheet");
const group = () => document.querySelector<HTMLElement>(".sheet .conn-grp")!;
const sw = () => group().querySelector<HTMLButtonElement>('[role="switch"]')!;
const statusRow = () => group().querySelector<HTMLElement>(".row.dx");
const footer = () => document.querySelector(".sheet .gf")!.textContent;

describe("the Module 3 summary row & unified Direct sheet frame", () => {
  it("shows a compact 直连 summary row on MachinePage and opens the unified sheet with 允许直连 switch", () => {
    open();
    expect(host.querySelector("select")).toBeNull();
    expect(summaryRow()).toBeTruthy();
    expect(summaryRow().querySelector(".chev")).toBeTruthy();
    expect(sheet()).toBeNull();
    act(() => summaryRow().click());
    expect(sheet()).not.toBeNull();
    expect(group().firstElementChild!.textContent).toContain(message("transport.row_allow_direct"));
    expect(group().querySelectorAll('[role="switch"]').length).toBe(1);
    expect(document.querySelectorAll(".sheet")).toHaveLength(1);
  });

  it("explains direct vs relay behavior cleanly in one sentence when healthy/off", () => {
    const r = openSheet();
    show(r, { state: "connected", file: "direct", read: "direct" });
    expect(footer()).toBe("发消息和审批同时经直连和中继发出，以先到的为准；后台时只用中继。");
    act(() => sw().click());
    expect(footer()).toBe("发消息和审批同时经直连和中继发出，以先到的为准；后台时只用中继。");
  });
});

describe("summary row + unified sheet status row", () => {
  it("updates summary row and sheet status row for green direct + RTT + single-purpose subtitle", () => {
    const r = openSheet();
    show(r, { state: "connected", file: "direct", read: "direct" });
    expect(summaryRow().querySelector("i.mark")!.className).toContain("online");
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("正在使用");
    const row = statusRow()!;
    expect(row.querySelector("i.mark")!.className).toContain("online");
    expect(row.querySelector(".dx-text")!.textContent).toBe("正在用直连");
    expect(row.querySelector("small")).toBeNull();
    expect(row.querySelector(".dx-rtt")).toBeNull();

    show(r, { state: "connected", read: "direct", readMs: { direct: 16 } });
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("正在使用 · 16 ms");
    expect(summaryRow().querySelector("small")!.textContent).toBe("用于加载");
    expect(statusRow()!.querySelector("small")!.textContent).toBe("用于加载");
    expect(statusRow()!.querySelector(".dx-rtt")!.textContent).toBe("16 ms");

    show(r, { state: "connected", file: "direct" });
    expect(statusRow()!.querySelector("small")!.textContent).toBe("用于文件");
  });

  it("shows gray dot for relay fallback with reason and no inline speed table", () => {
    const r = openSheet();
    show(r, { state: "connected", readSlow: true, fileSlow: true, fileMiBps: { direct: 2, relay: 4 }, readMs: { direct: 220, relay: 40 } });
    expect(summaryRow().querySelector("i.mark")!.className).toContain("off");
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("中继 · 40 ms");
    expect(statusRow()!.querySelector("i.mark")!.className).toContain("off");
    expect(statusRow()!.querySelector(".dx-text > .mo-swap")!.textContent).toBe("正在用中继");
    expect(statusRow()!.querySelector("small")!.textContent).toBe("直连较慢，暂时改用中继");
    expect(statusRow()!.querySelector(".dx-rtt")!.textContent).toBe("40 ms");
    expect(group().textContent).not.toContain("MiB/s");
  });

  it("shows spinning mark while connecting", () => {
    const r = openSheet();
    show(r, { state: "connecting" });
    expect(summaryRow().querySelector("i.mark")!.className).toContain("run");
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("正在建立直连…");
    expect(statusRow()!.querySelector("i.mark")!.className).toContain("run");
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("正在建立直连…");
  });

  it("renders inline diagnostics ONLY in Stage 4 failed relay, directly in the same sheet", () => {
    const r = openSheet();
    show(r, { state: "connected", file: "direct", read: "direct" });
    expect(sheet()!.querySelector(".diag-checks")).toBeNull();
    show(r, { state: "connecting" });
    expect(sheet()!.querySelector(".diag-checks")).toBeNull();
    show(r, { state: "connected", readSlow: true, fileSlow: true });
    expect(sheet()!.querySelector(".diag-checks")).toBeNull();
    show(r, { state: "none", reason: "no_traversal" });
    expect(sheet()!.querySelector(".diag-checks")).not.toBeNull();
    expect(document.querySelectorAll(".sheet")).toHaveLength(1);
  });

  it("is not drawn as connected for offline machine while keeping switch", () => {
    const r = openSheet("mini");
    expect(r.peer("mini").reachable).toBe(false);
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("离线");
    show(r, { state: "none", reason: "failed" });
    expect(statusRow()).toBeNull();
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });
});

describe("the switch inside the unified Direct sheet", () => {
  it("has accessible role, state, and 允许直连 label", () => {
    openSheet();
    expect(sw().tagName).toBe("BUTTON");
    expect(sw().getAttribute("aria-label")).toBe(message("transport.row_allow_direct"));
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });

  it("turns direct off immediately and closes diagnostics", () => {
    const r = openSheet();
    const set = vi.spyOn(r.direct, "setEnabled");
    show(r, { state: "connected", file: "direct", read: "direct" });
    act(() => sw().click());
    expect(set).toHaveBeenCalledWith("mac", false);
    expect(r.direct.enabled("mac")).toBe(false);
    vi.mocked(r.direct.status).mockRestore();
    notify(r);
    expect(sw().getAttribute("aria-checked")).toBe("false");
    expect(statusRow()).toBeNull();
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("已关闭");
  });

  it("says 正在建立直连… when turned back on, then reacts or falls back after 10s", () => {
    vi.useFakeTimers();
    try {
      const r = openSheet();
      act(() => sw().click());
      act(() => sw().click());
      expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("正在建立直连…");
      expect(statusRow()!.querySelector("i.mark")!.className).toContain("run");
      act(() => { vi.advanceTimersByTime(10_001); });
      expect(statusRow()!.querySelector(".dx-text > .mo-swap")!.textContent).toBe("正在用中继");
      expect(statusRow()!.querySelector("small")!.textContent).toBe("安全状态待刷新");
      act(() => sw().click());
      act(() => sw().click());
      setDirect(r.direct, "mac", "failed", "no_traversal");
      notify(r);
      expect(statusRow()!.querySelector("small")!.textContent).toBe("两边的网络隔开了，不能直接连通");
    } finally { vi.useRealTimers(); }
  });

  it("shows save error and keeps switch state on failure", () => {
    const r = openSheet();
    vi.spyOn(r.direct, "setEnabled").mockImplementation(() => { throw new Error("存不下"); });
    act(() => sw().click());
    expect(sheet()!.querySelector(".banner, [class*=banner]")!.textContent).toContain("存不下");
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });

  it("greys out switch when browser lacks WebRTC and translates to English cleanly", () => {
    vi.stubGlobal("RTCPeerConnection", undefined);
    const r = openSheet();
    const set = vi.spyOn(r.direct, "setEnabled");
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("不可用");
    expect(sw().disabled).toBe(true);
    expect(sw().getAttribute("aria-checked")).toBe("false");
    expect(statusRow()!.querySelector(".dx-text > .mo-swap")!.textContent).toBe("这个浏览器不支持直连");
    act(() => sw().click());
    expect(set).not.toHaveBeenCalled();

    act(() => root?.unmount());
    host.remove();
    vi.stubGlobal("RTCPeerConnection", class {});
    localeStore.overrideForTesting("en");
    const rEn = openSheet();
    show(rEn, { state: "connected", file: "direct", read: "direct", readMs: { direct: 12 } });
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("Direct · 12 ms");
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("Using direct");
    expect(footer()).toBe("Messages and approvals are sent over both direct and relay, taking whichever arrives first; only relay is used in the background.");
  });
});

describe("the demo's ?direct= states", () => {
  it.each([
    ["direct", "正在用直连", undefined],
    ["file", "正在用直连", "用于文件"],
    ["read", "正在用直连", "用于加载"],
    ["file-fallback", "正在用直连", "用于加载"],
    ["relay", "正在用中继", "直连较慢，暂时改用中继"],
    ["trying", "正在建立直连…", undefined],
    ["none", "正在用中继", "正在准备直连"],
    ["peer_unsupported", "正在用中继", "电脑上的版本太旧，更新后可用"],
    ["no_response", "正在用中继", "电脑没有回应，可能在电脑上关了直连"],
    ["no_traversal", "正在用中继", "两边的网络隔开了，不能直接连通"],
    ["failed", "正在用中继", "没连上，稍后会自动再试"],
  ])("direct=%s", (name, w, s) => {
    openSheet("mac", `direct=${name}`);
    expect([statusRow()?.querySelector(".dx-text > .mo-swap")?.textContent, statusRow()?.querySelector("small")?.textContent]).toEqual([w, s]);
  });

  it("auto-opens the unified sheet when navigated with section=direct or section=connection", () => {
    open("mac", "", "direct");
    expect(sheet()).not.toBeNull();
    expect(sheet()!.querySelector(".sheet-head b")!.textContent).toBe(message("transport.direct"));
    act(() => root?.unmount());
    host.remove();
    open("mac", "", "connection");
    expect(sheet()).not.toBeNull();
    expect(sheet()!.querySelector(".sheet-head b")!.textContent).toBe(message("transport.direct"));
  });
});
