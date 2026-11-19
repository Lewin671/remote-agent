// @vitest-environment happy-dom
// 诊断信息: the last row of the 「连接」 group and the sheet it opens. The data is
// the demo's ?diag= scenarios, played through the real collector.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { localeStore } from "../i18n";
import { MachinePage } from "./Settings";
import { diagView } from "./direct-diag-view";

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
const group = () => host.querySelector<HTMLElement>(".conn-grp")!;
const sheet = () => document.querySelector<HTMLElement>(".sheet");
const heroTitle = () => sheet()!.querySelector(".diag-hero h5")!.textContent;
const heroBody = () => sheet()!.querySelector(".diag-hero p")!.textContent;
const checks = () => [...sheet()!.querySelectorAll<HTMLElement>(".diag-check")].map((el) => ({
  label: el.querySelector(".diag-check-b b")!.textContent!,
  detail: el.querySelector(".diag-check-b small")?.textContent,
  status: el.querySelector(".diag-check-st")!.textContent!,
  mark: el.querySelector(".mark")?.className ?? "",
}));
const retryBtn = () => sheet()!.querySelector<HTMLButtonElement>(".diag-actions .diag-retry")!;
const copyBtn = () => sheet()!.querySelector<HTMLButtonElement>(".diag-actions .diag-copy")!;

describe("the row", () => {
  it("is the last row of the 「连接」 group, a navigation row with a chevron that opens the sheet", () => {
    open("diag=ipv6");
    const last = group().lastElementChild as HTMLElement;
    expect(last.textContent).toBe("为什么没连上");
    expect(last.tagName).toBe("BUTTON");
    expect(last.querySelector(".chev")).not.toBeNull();
    expect(sheet()).toBeNull();
    act(() => last.click());
    expect(sheet()).not.toBeNull();
    expect(sheet()!.querySelector(".sheet-head b")!.textContent).toBe("为什么没连上");
  });

  it("is not there while direct is off or the browser cannot do it", () => {
    open("direct=off");
    expect(group().textContent).not.toContain("为什么没连上");
    act(() => root?.unmount()); host.remove();
    open("direct=unsupported");
    expect(group().textContent).not.toContain("为什么没连上");
  });
});

describe("the sheet for each failure class", () => {
  it.each([
    ["ipv6", "两边的网络隔开了，不能直接连通"],
    ["all", "两边的网络隔开了，不能直接连通"],
    ["nolocal", "所在网络限制了直连探测，不能直接连通"],
    ["nostun", "所在网络限制了直连探测，不能直接连通"],
    ["noremote", "两边的网络隔开了，不能直接连通"],
    ["dtls", "两边的网络隔开了，不能直接连通"],
    ["channel", "两边的网络隔开了，不能直接连通"],
    ["refused", "电脑上的版本太旧，更新后可用"],
    ["noreply", "电脑没有回应直连握手，可能在电脑上关了直连"],
    ["limited", "暂时没连上，稍后会自动再试"],
    ["budget", "暂时没连上，稍后会自动再试"],
    ["connected", "直连已连上"],
    ["trying", "暂时没连上，稍后会自动再试"],
  ])("%s", (name, expected) => {
    open(`diag=${name}&diagsheet=1`);
    expect(heroTitle()).toBe(expected);
    if (name === "connected") expect(heroBody()).toContain("恢复直连");
    else expect(heroBody()).toContain("不影响正常使用");
  });

  it("shows 3 plain-language self-check rows and hides low-level ICE/DTLS/IPv4/IPv6 jargon", () => {
    open("diag=ipv6&diagsheet=1");
    expect(checks().map((c) => [c.label, c.status])).toEqual([
      ["电脑在线并响应", "正常"],
      ["局域网或公网直通", "未通"],
      ["后台自动重试", "已开启"],
    ]);
    const body = sheet()!.textContent!;
    expect(body).not.toMatch(/ICE 状态|IPv4|IPv6|DTLS|SCTP|已隐藏的地址|Candidate/);
  });

  it("shows probe-blocked reason in the path self-check on nostun", () => {
    open("diag=nostun&diagsheet=1");
    expect(heroTitle()).toBe("所在网络限制了直连探测，不能直接连通");
    expect(checks()[1]).toMatchObject({ label: "局域网或公网直通", status: "未通", detail: "UDP 直连探测被当前网络拦截" });
  });

  it("never shows an address or a port in the UI", () => {
    for (const name of ["ipv6", "all", "nostun", "dtls"]) {
      open(`diag=${name}&diagsheet=1`);
      const text = sheet()!.textContent!;
      expect(text).not.toMatch(/\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}|2001|db8|abcd|5000|\.local/);
      act(() => root?.unmount()); host.remove();
    }
  });

  it("has a privacy line at the foot", () => {
    open("diag=ipv6&diagsheet=1");
    expect(sheet()!.querySelector(".gf")!.textContent).toBe("复制的诊断只含地址类型和范围，不含具体地址、端口或对话内容。");
  });
});

describe("复制诊断给支持", () => {
  const copyTo = () => {
    const writeText = vi.fn(async (_text: string) => undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    return writeText;
  };

  it("copies sanitized plain text for support (all rounds, masked), then says 已复制", async () => {
    const writeText = copyTo();
    open("diag=ipv6&diagsheet=1");
    expect(copyBtn().textContent).toBe("复制诊断给支持");
    await act(async () => copyBtn().click());
    const text = writeText.mock.calls[0][0];
    expect(text.split("\n")[0]).toBe("直连诊断");
    expect(text).toContain("结论：本机拿到了 IPv6，但电脑的 IPv6 地址没有回应");
    expect(text).toContain("第 5 次 · 刚刚");
    expect(text).toContain("第 3 次 · 3 分钟前");
    expect(text).toMatch(/加载 · 停在: 连通检查/);
    expect(text).toMatch(/文件 · 停在: 连通检查/);
    expect(text).toContain("IPv6 → IPv6: 失败 1 · 发出 14 · 收到 0");
    expect(text).not.toMatch(/\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}|2001|db8|abcd|5000|\.local/);
    expect(copyBtn().textContent).toBe("已复制");
  });

  it("says 没能复制，请检查剪贴板权限 when the clipboard refuses", async () => {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn(async () => { throw new Error("no"); }) } });
    open("diag=ipv6&diagsheet=1");
    await act(async () => copyBtn().click());
    expect(copyBtn().textContent).toBe("没能复制，请检查剪贴板权限");
  });
});

describe("重新尝试", () => {
  it("runs one round at once, disables button and spins 正在尝试…, and disables when connected or refused", () => {
    const r = open("diag=ipv6&diagsheet=1");
    const retry = vi.spyOn(r.direct, "retryNow");
    expect(retryBtn().disabled).toBe(false);
    expect(retryBtn().textContent).toBe("重新尝试");
    act(() => retryBtn().click());
    expect(retry).toHaveBeenCalledWith("mac");
    expect(retryBtn().disabled).toBe(true);
    expect(retryBtn().textContent).toBe("正在尝试…");
    expect(checks()[1]).toMatchObject({ label: "局域网或公网直通", status: "正在检查…" });
    act(() => root?.unmount()); host.remove();
    open("diag=connected&diagsheet=1");
    expect(retryBtn().disabled).toBe(true);
    act(() => root?.unmount()); host.remove();
    open("diag=refused&diagsheet=1");
    expect(retryBtn().disabled).toBe(true);
  });
});

describe("in English", () => {
  it("says the same things with plain checks", () => {
    localeStore.overrideForTesting("en");
    open("diag=ipv6&diagsheet=1");
    expect(sheet()!.querySelector(".sheet-head b")!.textContent).toBe("Why it didn’t connect");
    expect(heroTitle()).toBe("The two networks can't reach each other directly");
    expect(checks().map((c) => c.label)).toEqual(["Computer reachable & responding", "Local or public direct path", "Background auto-retry"]);
    expect(copyBtn().textContent).toBe("Copy diagnostics for support");
    expect(retryBtn().textContent).toBe("Try again");
  });
});

describe("diagView", () => {
  it("is empty and honest before any round", () => {
    const v = diagView({ rounds: [], now: 0 });
    expect(v.sentence).toBe("还没有直连尝试的记录");
    expect(v.groups).toEqual([]);
    expect(v.result.rows).toEqual([]);
  });

  it("describes the newest finished round while a newer one is still going", () => {
    const r = open("diag=ipv6");
    r.direct.retryNow("mac");
    const rounds = r.direct.diagnostics("mac").rounds;
    const v = diagView({ rounds: [{ ...rounds[0], end: undefined, endedAt: undefined }, ...rounds], now: Date.now() });
    expect(v.sentence).toBe("本机拿到了 IPv6，但电脑的 IPv6 地址没有回应");
    expect(v.result.rows[1].value).toMatch(/^进行中/);
  });
});
