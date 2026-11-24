// @vitest-environment happy-dom
// Module 3 「直连」 summary row + unified DirectSettingsSheet (Core switch + status + inline diagnostics).

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getCoreHost } from "../core/host";
import { DemoRemote } from "../dev/demo";
import { clearDemoDirect, setDemoDirect, type DemoDirectSpec } from "../dev/direct-demo";
import { localeStore, t as message } from "../i18n";
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
function show(r: DemoRemote, spec: DemoDirectSpec | Parameters<typeof setDemoDirect>[2]) {
  act(() => setDemoDirect(r, "mac", spec));
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
    show(r, "direct");
    expect(footer()).toBe("发消息、审批和终端输入同时经直连和中继发出，以先到的为准；后台时只用中继。");
    act(() => clearDemoDirect(r, "mac"));
    act(() => sw().click());
    expect(footer()).toBe("发消息、审批和终端输入同时经直连和中继发出，以先到的为准；后台时只用中继。");
  });
});

describe("summary row + unified sheet status row", () => {
  it("updates summary row and sheet status row for green direct + RTT + single-purpose subtitle", () => {
    const r = openSheet();
    show(r, "direct");
    expect(summaryRow().querySelector("i.mark")!.className).toContain("online");
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("正在使用");
    const row = statusRow()!;
    expect(row.querySelector("i.mark")!.className).toContain("online");
    expect(row.querySelector(".dx-text")!.textContent).toBe("正在用直连");
    expect(row.querySelector("small")).toBeNull();
    expect(row.querySelector(".dx-rtt")).toBeNull();

    show(r, { preset: "read", rttMs: 16 });
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("正在使用 · 16 ms");
    expect(summaryRow().querySelector("small")!.textContent).toBe("用于加载");
    expect(statusRow()!.querySelector("small")!.textContent).toBe("用于加载");
    expect(statusRow()!.querySelector(".dx-rtt")!.textContent).toBe("16 ms");

    show(r, "file");
    expect(statusRow()!.querySelector("small")!.textContent).toBe("用于文件");
  });

  it("shows gray dot for relay fallback with reason and no inline speed table", () => {
    const r = openSheet();
    show(r, { preset: "relay-faster", rttMs: 40 });
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
    show(r, "trying");
    expect(summaryRow().querySelector("i.mark")!.className).toContain("run");
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("正在建立直连…");
    expect(statusRow()!.querySelector("i.mark")!.className).toContain("run");
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("正在建立直连…");
  });

  it("renders inline diagnostics ONLY in Stage 4 failed relay", () => {
    const r = openSheet();
    show(r, "direct");
    expect(sheet()!.querySelector(".diag-checks")).toBeNull();
    show(r, "trying");
    expect(sheet()!.querySelector(".diag-checks")).toBeNull();
    show(r, "relay-faster");
    expect(sheet()!.querySelector(".diag-checks")).toBeNull();
    show(r, "no_traversal");
    expect(sheet()!.querySelector(".diag-checks")).not.toBeNull();
    expect(sheet()!.querySelectorAll(".diag-check")).toHaveLength(3);
    expect(document.querySelectorAll(".sheet")).toHaveLength(1);
  });

  it("wires retry + copy in Stage 4 failed relay diagnostics", async () => {
    const r = openSheet("mac", "direct=no_traversal");
    const retrySpy = vi.spyOn(getCoreHost(r), "retryDirectNow").mockResolvedValue(false);
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });

    await act(async () => {
      sheet()!.querySelector<HTMLButtonElement>(".diag-retry")!.click();
    });
    expect(retrySpy).toHaveBeenCalledWith("mac");

    await act(async () => {
      sheet()!.querySelector<HTMLButtonElement>(".diag-copy")!.click();
    });
    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText.mock.calls[0]?.[0]).toContain(message("transport.diag_verdict_all_failed"));
  });

  it("is not drawn as connected for offline machine while keeping switch", () => {
    const r = openSheet("mini");
    expect(r.peer("mini").reachable).toBe(false);
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("离线");
    act(() => setDemoDirect(r, "mini", "failed"));
    expect(statusRow()).toBeNull();
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });
});

describe("the switch inside the unified Direct sheet (real Core)", () => {
  it("has accessible role, state, and 允许直连 label", () => {
    openSheet();
    expect(sw().tagName).toBe("BUTTON");
    expect(sw().getAttribute("aria-label")).toBe(message("transport.row_allow_direct"));
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });

  it("turns direct off immediately in Core and closes status/diagnostics", () => {
    const r = openSheet();
    const coreHost = getCoreHost(r);
    const upd = vi.spyOn(coreHost, "updateDirectPreference");
    act(() => sw().click());
    expect(upd).toHaveBeenCalledWith("mac", false);
    expect(coreHost.viewMachineSettings("mac").connection?.toggleChecked).toBe(false);
    expect(sw().getAttribute("aria-checked")).toBe("false");
    expect(statusRow()).toBeNull();
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("已关闭");
  });

  it("says 正在建立直连… when turned back on, then reacts or falls back after 10s", () => {
    vi.useFakeTimers();
    let r: ReturnType<typeof openSheet> | undefined;
    try {
      r = openSheet();
      act(() => sw().click());
      act(() => sw().click());
      expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("正在建立直连…");
      expect(statusRow()!.querySelector("i.mark")!.className).toContain("run");
      act(() => { vi.advanceTimersByTime(10_001); });
      expect(statusRow()!.querySelector(".dx-text > .mo-swap")!.textContent).toBe("正在用中继");
      expect(statusRow()!.querySelector("small")!.textContent).toBe("安全状态待刷新");
      act(() => sw().click());
      act(() => sw().click());
      show(r, "no_traversal");
      expect(statusRow()!.querySelector("small")!.textContent).toBe("两边的网络隔开了，不能直接连通");
    } finally {
      act(() => root?.unmount());
      if (r) {
        host.remove();
        r.dispose();
      }
      vi.clearAllTimers();
      vi.useRealTimers();
    }
  });

  it("shows save error and reverts Core switch state on storage failure", () => {
    localStorage.clear();
    openSheet();
    const realStorage = localStorage;
    vi.stubGlobal("localStorage", {
      get length() { return realStorage.length; },
      key: (i: number) => realStorage.key(i),
      getItem: (k: string) => realStorage.getItem(k),
      removeItem: (k: string) => realStorage.removeItem(k),
      clear: () => realStorage.clear(),
      setItem: () => { throw new Error("存不下"); },
    });
    act(() => sw().click());
    expect(sheet()!.querySelector(".banner, [class*=banner]")!.textContent).toContain("存不下");
    expect(sw().getAttribute("aria-checked")).toBe("true");
  });

  it("greys out switch when browser lacks WebRTC and translates to English cleanly", () => {
    vi.stubGlobal("RTCPeerConnection", undefined);
    const r = openSheet();
    const upd = vi.spyOn(getCoreHost(r), "updateDirectPreference");
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("不可用");
    expect(sw().disabled).toBe(true);
    expect(sw().getAttribute("aria-checked")).toBe("false");
    expect(statusRow()!.querySelector(".dx-text > .mo-swap")!.textContent).toBe("这个浏览器不支持直连");
    act(() => sw().click());
    expect(upd).not.toHaveBeenCalled();

    act(() => root?.unmount());
    host.remove();
    vi.stubGlobal("RTCPeerConnection", class {});
    localeStore.overrideForTesting("en");
    const rEn = openSheet();
    show(rEn, { preset: "direct", rttMs: 12 });
    expect(summaryRow().querySelector(".vt")!.textContent).toBe("Direct · 12 ms");
    expect(statusRow()!.querySelector(".dx-text")!.textContent).toBe("Using direct");
    expect(footer()).toBe("Messages, approvals, and terminal input are sent over both direct and relay, taking whichever arrives first; only relay is used in the background.");
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
