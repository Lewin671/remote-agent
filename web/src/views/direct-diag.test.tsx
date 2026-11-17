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
const sentence = () => sheet()!.querySelector(".diag-verdict")!.textContent;
const titles = () => [...sheet()!.querySelectorAll(".fl")].map((e) => e.textContent);
/** The rows of one group of the sheet: label -> value. */
function rows(title: string): [string, string][] {
  const label = [...sheet()!.querySelectorAll(".fl")].find((e) => e.textContent === title)!;
  return [...label.nextElementSibling!.querySelectorAll(".mi")].map((m) => [m.querySelector(".mt")!.textContent!, m.querySelector(".mv")!.textContent!]);
}
const action = (name: string) => [...sheet()!.querySelectorAll<HTMLButtonElement>(".diag-acts .mi")].find((b) => b.textContent === name)!;

describe("the row", () => {
  it("is the last row of the 「连接」 group, a navigation row with a chevron that opens the sheet", () => {
    open("diag=ipv6");
    const last = group().lastElementChild as HTMLElement;
    expect(last.textContent).toBe("诊断信息");
    expect(last.tagName).toBe("BUTTON");
    expect(last.querySelector(".chev")).not.toBeNull();
    expect(sheet()).toBeNull();
    act(() => last.click());
    expect(sheet()).not.toBeNull();
    expect(sheet()!.querySelector(".sheet-head b")!.textContent).toBe("诊断信息");
  });

  it("is not there while direct is off or the browser cannot do it", () => {
    open("direct=off");
    expect(group().textContent).not.toContain("诊断信息");
    act(() => root?.unmount()); host.remove();
    open("direct=unsupported");
    expect(group().textContent).not.toContain("诊断信息");
  });
});

describe("the sheet for each failure class", () => {
  it.each([
    ["ipv6", "本机拿到了 IPv6，但电脑的 IPv6 地址没有回应"],
    ["all", "两边地址都有，连通检查全部失败"],
    ["nolocal", "本机没有拿到任何可用地址"],
    ["nostun", "本机没有拿到公网映射地址，网络可能挡了 STUN"],
    ["noremote", "没有收到电脑的任何地址"],
    ["dtls", "已连上，加密握手没完成"],
    ["channel", "加密握手完成，数据通道没有打开"],
    ["refused", "电脑拒绝了这次直连（版本、策略或数量上限）"],
    ["noreply", "电脑没有回应直连握手，可能在电脑上关了直连"],
    ["limited", "握手消息被中继限速了，稍后会自动再试"],
    ["budget", "本机的直连名额已满，这次没有尝试"],
    ["connected", "直连已连上"],
    ["trying", "正在建立直连…"],
  ])("%s", (name, expected) => {
    open(`diag=${name}&diagsheet=1`);
    expect(sentence()).toBe(expected);
  });

  it("groups the rows as 本机地址 / 电脑地址 / 连通检查 / 结果 with the counts of the lead path", () => {
    open("diag=ipv6&diagsheet=1");
    expect(titles()).toEqual(["本机地址", "电脑地址", "连通检查", "结果"]);
    expect(rows("本机地址").slice(0, 3)).toEqual([
      ["IPv4", "本机网卡 ×1（私网） · 公网映射 ×1（公网）"],
      ["IPv6", "本机网卡 ×1（全球）"],
      ["已隐藏的地址", "×1"],
    ]);
    expect(rows("电脑地址")).toEqual([["IPv4", "本机网卡 ×1（私网） · 公网映射 ×1（公网）"], ["IPv6", "本机网卡 ×1（全球）"]]);
    const checks = rows("连通检查");
    expect(checks[0]).toEqual(["IPv6 → IPv6", "失败 1 · 发出 14 · 收到 0"]);
    expect(checks.map((c) => c[0])).toContain("ICE 状态");
    const result = rows("结果");
    expect(result[0]).toEqual(["停在", "连通检查"]);
    // Three recorded rounds, the newest first.
    expect(result.slice(1).map((r) => r[1])).toEqual(["没连上 · 9.9 秒", "没连上 · 9.9 秒", "电脑没回应 · 10.0 秒"]);
    expect(result.slice(1).map((r) => r[0])).toEqual(["刚刚", "1 分钟前", "3 分钟前"]);
  });

  it("says STUN errors and refused messages where they belong", () => {
    open("diag=nostun&diagsheet=1");
    expect(rows("本机地址").find((r) => r[0] === "STUN 错误")).toEqual(["STUN 错误", "701 · IPv4 ×2"]);
  });

  it("never shows an address or a port, only types and ranges", () => {
    for (const name of ["ipv6", "all", "nostun", "dtls"]) {
      open(`diag=${name}&diagsheet=1`);
      const text = sheet()!.textContent!;
      expect(text).not.toMatch(/\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}|2001|db8|abcd|5000|\.local/);
      act(() => root?.unmount()); host.remove();
    }
  });

  it("has a privacy line at the foot", () => {
    open("diag=ipv6&diagsheet=1");
    expect(sheet()!.querySelector(".gf")!.textContent).toBe("诊断信息只包含地址的类型和范围，不含具体地址。");
  });
});

describe("复制诊断信息", () => {
  const copyTo = () => {
    const writeText = vi.fn(async (_text: string) => undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    return writeText;
  };

  it("copies plain text with the same data for every round and both paths, still masked, then says 已复制", async () => {
    const writeText = copyTo();
    open("diag=ipv6&diagsheet=1");
    await act(async () => action("复制诊断信息").click());
    const text = writeText.mock.calls[0][0];
    expect(text.split("\n")[0]).toBe("直连诊断");
    expect(text).toContain("结论：本机拿到了 IPv6，但电脑的 IPv6 地址没有回应");
    expect(text).toContain("第 5 次 · 刚刚");
    expect(text).toContain("第 3 次 · 3 分钟前");
    expect(text).toMatch(/加载 · 停在: 连通检查/);
    expect(text).toMatch(/文件 · 停在: 连通检查/);
    expect(text).toContain("IPv6 → IPv6: 失败 1 · 发出 14 · 收到 0");
    expect(text).not.toMatch(/\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}|2001|db8|abcd|5000|\.local/);
    expect(action("已复制")).toBeDefined();
  });

  it("says so when the clipboard refuses", async () => {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn(async () => { throw new Error("no"); }) } });
    open("diag=ipv6&diagsheet=1");
    await act(async () => action("复制诊断信息").click());
    expect(action("没能复制")).toBeDefined();
  });
});

describe("重新尝试", () => {
  it("runs one round at once and is greyed while one is being set up or up", () => {
    const r = open("diag=ipv6&diagsheet=1");
    const retry = vi.spyOn(r.direct, "retryNow");
    expect(action("重新尝试").disabled).toBe(false);
    act(() => action("重新尝试").click());
    expect(retry).toHaveBeenCalledWith("mac");
    // The demo shows a round in progress: the retry is greyed and the newest round is listed as in progress.
    expect(action("重新尝试").disabled).toBe(true);
    expect(rows("结果").find((x) => x[1].startsWith("进行中"))).toBeDefined();
    act(() => root?.unmount()); host.remove();
    open("diag=connected&diagsheet=1");
    expect(action("重新尝试").disabled).toBe(true);
  });
});

describe("in English", () => {
  it("says the same things with the same rows", () => {
    localeStore.overrideForTesting("en");
    open("diag=ipv6&diagsheet=1");
    expect(sheet()!.querySelector(".sheet-head b")!.textContent).toBe("Diagnostics");
    expect(sentence()).toBe("This device has IPv6, but the computer's IPv6 address didn't answer");
    expect(titles()).toEqual(["This device's addresses", "The computer's addresses", "Connectivity checks", "Result"]);
    expect(rows("This device's addresses")[1]).toEqual(["IPv6", "Local interface ×1 (global)"]);
    expect(rows("Connectivity checks")[0]).toEqual(["IPv6 → IPv6", "1 failed · sent 14 · got 0"]);
    expect(rows("Result")[0]).toEqual(["Stopped at", "Connectivity checks"]);
    expect(action("Copy diagnostics")).toBeDefined();
    expect(action("Try again")).toBeDefined();
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
