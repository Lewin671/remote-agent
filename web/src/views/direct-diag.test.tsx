// @vitest-environment happy-dom
// Unified Direct Sheet inline diagnostics: 3-row auto self-check, retry, and copy without a nested sheet.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { localeStore } from "../i18n";
import { MachinePage } from "./Settings";
import { diagView, plainDiagView } from "./direct-diag-view";

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
function open(query: string, id = "mac"): DemoRemote {
  const r = DemoRemote.create(new URLSearchParams(query), async () => undefined);
  remotes.push(r);
  r.start();
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(<MachinePage remote={r} id={id} go={none} onBack={none} />));
  return r;
}
const directRow = () =>
  [...host.querySelectorAll<HTMLButtonElement>("button.row")].find(
    (b) => b.querySelector(".row-name b")?.textContent === "直连",
  )!;
const sheet = () => document.querySelector<HTMLElement>(".sheet");
const checks = () => [...sheet()!.querySelectorAll<HTMLElement>(".diag-check")].map((el) => ({
  label: el.querySelector(".diag-check-b b")!.textContent!,
  detail: el.querySelector(".diag-check-b small")?.textContent,
  status: el.querySelector(".diag-check-st")!.textContent!,
  mark: el.querySelector(".mark")?.className ?? "",
}));
const retryBtn = () => sheet()!.querySelector<HTMLButtonElement>(".diag-actions .diag-retry")!;
const copyBtn = () => sheet()!.querySelector<HTMLButtonElement>(".diag-actions .diag-copy")!;

describe("the unified sheet & inline diagnostics section", () => {
  it("opens one unified sheet from 直连 showing toggle + route + inline diagnostics with NO nested sheet", () => {
    open("diag=ipv6");
    expect(sheet()).toBeNull();
    act(() => directRow().click());
    expect(document.querySelectorAll(".sheet")).toHaveLength(1);
    expect(sheet()!.querySelector(".sheet-head b")!.textContent).toBe("直连");
    expect(sheet()!.textContent).toContain("允许直连");
    expect(sheet()!.textContent).toContain("自动排障检查");
  });

  it("hides diagnostics when direct is off or the browser lacks WebRTC", () => {
    open("direct=off");
    act(() => directRow().click());
    expect(sheet()!.querySelector(".diag-checks")).toBeNull();
    act(() => root?.unmount());
    host.remove();
    open("direct=unsupported");
    act(() => directRow().click());
    expect(sheet()!.querySelector(".diag-checks")).toBeNull();
  });
});

describe("the inline self-checks across failure scenarios", () => {
  it("renders 3 plain-language self-check rows and hides ICE/DTLS/IPv4/IPv6/Candidate jargon", () => {
    open("diag=ipv6&diagsheet=1");
    expect(checks().map((c) => [c.label, c.status])).toEqual([
      ["电脑在线并响应", "正常"],
      ["局域网或公网直通", "未通"],
      ["后台自动重试", "已开启"],
    ]);
    const body = sheet()!.textContent!;
    expect(body).not.toMatch(/ICE 状态|IPv4|IPv6|DTLS|SCTP|已隐藏的地址|Candidate/);
  });

  it("shows probe-blocked detail on nostun and noreply / refused details cleanly", () => {
    open("diag=nostun&diagsheet=1");
    expect(checks()[1]).toMatchObject({ label: "局域网或公网直通", status: "未通", detail: "UDP 直连探测被当前网络拦截" });
    act(() => root?.unmount());
    host.remove();
    open("diag=refused&diagsheet=1");
    expect(checks()[0]).toMatchObject({ label: "电脑在线并响应", status: "版本过旧" });
    expect(retryBtn().disabled).toBe(true);
    act(() => root?.unmount());
    host.remove();
    open("diag=noreply&diagsheet=1");
    expect(checks()[0]).toMatchObject({ label: "电脑在线并响应", status: "未响应" });
  });

  it("never exposes an address or a port in the UI, and keeps the privacy line at the foot", () => {
    for (const name of ["ipv6", "all", "nostun", "dtls"]) {
      open(`diag=${name}&diagsheet=1`);
      const text = sheet()!.textContent!;
      expect(text).not.toMatch(/\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}|2001|db8|abcd|5000|\.local/);
      expect(sheet()!.querySelector(".gf")!.textContent).toBe("复制的诊断只含地址类型和范围，不含具体地址、端口或对话内容。");
      act(() => root?.unmount());
      host.remove();
    }
  });
});

describe("复制诊断给支持 & 重新尝试直连", () => {
  const copyTo = () => {
    const writeText = vi.fn(async (_text: string) => undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    return writeText;
  };

  it("copies sanitized plain text for support, then displays 已复制脱敏诊断", async () => {
    const writeText = copyTo();
    open("diag=ipv6&diagsheet=1");
    expect(copyBtn().textContent).toBe("复制诊断给支持");
    await act(async () => copyBtn().click());
    const text = writeText.mock.calls[0][0];
    expect(text.split("\n")[0]).toBe("直连诊断");
    expect(text).toContain("结论：本机拿到了 IPv6，但电脑的 IPv6 地址没有回应");
    expect(text).toContain("IPv6 → IPv6: 失败 1 · 发出 14 · 收到 0");
    expect(text).not.toMatch(/\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}|2001|db8|abcd|5000|\.local/);
    expect(copyBtn().textContent).toBe("已复制脱敏诊断");
  });

  it("says 没能复制，请检查剪贴板权限 when the clipboard refuses", async () => {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn(async () => { throw new Error("no"); }) } });
    open("diag=ipv6&diagsheet=1");
    await act(async () => copyBtn().click());
    expect(copyBtn().textContent).toBe("没能复制，请检查剪贴板权限");
  });

  it("runs retry directly inside the sheet with spinning label", () => {
    const r = open("diag=ipv6&diagsheet=1");
    const retry = vi.spyOn(r.direct, "retryNow");
    expect(retryBtn().disabled).toBe(false);
    expect(retryBtn().textContent).toBe("重新尝试直连");
    act(() => retryBtn().click());
    expect(retry).toHaveBeenCalledWith("mac");
    expect(retryBtn().disabled).toBe(true);
    expect(retryBtn().textContent).toBe("正在尝试…");
    expect(checks()[1]).toMatchObject({ label: "局域网或公网直通", status: "正在检查…" });
  });

  it("works in English as well", () => {
    localeStore.overrideForTesting("en");
    open("diag=ipv6&diagsheet=1");
    expect(sheet()!.querySelector(".sheet-head b")!.textContent).toBe("Direct connection");
    expect(checks().map((c) => c.label)).toEqual([
      "Computer reachable & responding",
      "Local or public direct path",
      "Background auto-retry",
    ]);
    expect(copyBtn().textContent).toBe("Copy diagnostics for support");
    expect(retryBtn().textContent).toBe("Retry direct connection");
  });
});

describe("diagView & plainDiagView", () => {
  it("produces human-readable titles and sanitized summaries", () => {
    const empty = diagView({ rounds: [], now: 0 });
    expect(empty.sentence).toBe("还没有直连尝试的记录");
    expect(empty.groups).toEqual([]);
    const p = plainDiagView({ rounds: [], now: 0, reason: "no_traversal", connectedNow: false, trying: false });
    expect(p.title).toBe("两边的网络隔开了，不能直接连通");
  });
});
