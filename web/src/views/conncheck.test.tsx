import { localeStore, t as message } from "../i18n";
// @vitest-environment happy-dom
// 连接检测: each stretch's round trip, measured when the sheet opens.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { setDirect } from "../test/direct-state";
import { ConnCheck, median } from "./ConnCheck";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLElement;
const remotes: DemoRemote[] = [];
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  for (const r of remotes.splice(0)) r.dispose();
});

function open(query: string, patch?: (r: DemoRemote) => void) {
  const r = DemoRemote.create(new URLSearchParams(query));
  remotes.push(r);
  r.start();
  patch?.(r);
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(<ConnCheck remote={r} onClose={() => undefined} />));
  return r;
}
const directRows = () => {
  const out: Record<string, string> = {};
  for (const el of host.querySelectorAll<HTMLElement>(".conn-check .mi.conn-direct")) {
    const id = el.dataset.machineId!;
    out[id] = el.querySelector(".mt")?.textContent?.trim() ?? "";
  }
  return out;
};
const rows = () => Object.fromEntries(
  [...host.querySelectorAll<HTMLElement>(".conn-check .mi:not(.conn-direct)")]
    .map((e) => [e.firstElementChild!.firstChild!.textContent, e.lastElementChild!.textContent!.trim()]),
);

describe("connection check", () => {
  it("takes the middle sample", () => {
    expect(median([900, 80, 95])).toBe(95);
    expect(median([70])).toBe(70);
  });

  it("measures the server, then each online computer, and calls a long round trip slow", async () => {
    const pings = [900, 80, 95];
    open("", (r) => {
      r.pingServer = async () => pings.shift()!;
      r.pingMachine = async (id) => (id === "vps" ? 2140 : 180);
    });
    expect(rows()[message("security.server")]).toBe(message("connection.checking"));
    await vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>(".conn-again button")!.disabled).toBe(false));
    const got = rows();
    expect(got[message("connection.local_network")]).toBe(message("connection.working"));
    // One slow sample of three does not make the line slow.
    expect(got[message("security.server")]).toBe("95 ms");
    expect(got["MacBook"]).toBe("180 ms");
    expect(got["VPS-dev"]).toBe("2140 ms · 较慢");
    expect(got["Mac mini"]).toBe(message("common.ios_offline"));
  });

  it("says the server did not answer, and leaves the computers unmeasured", async () => {
    let asked = 0;
    open("", (r) => {
      r.pingServer = async () => Promise.reject(new Error("服务器没有回应"));
      r.pingMachine = async () => ++asked;
    });
    await vi.waitFor(() => expect(rows()[message("security.server")]).toBe(message("connection.no_response")));
    expect(rows()["MacBook"]).toBe(message("connection.not_checked"));
    expect(asked).toBe(0);
  });
});

describe("direct path rows", () => {
  const settle = () => vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>(".conn-again button")!.disabled).toBe(false));
  const quiet = (r: DemoRemote) => { r.pingServer = async () => 40; r.pingMachine = async () => 180; };
  const footer = () => host.querySelector(".gf")!.textContent!;
  const base = () => message("connection.latency_is_one_round_trip_the_server_row_measures");
  beforeEach(() => vi.stubGlobal("RTCPeerConnection", class {}));
  afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });

  it("shows security_pending reason rather than blank 正在用中继 when security is not fresh", async () => {
    open("", quiet);
    await settle();
    expect(directRows()["mac"]).toBe("正在用中继 · 安全状态待刷新");
  });

  it("names the cause in the direct row, and omits direct rows for offline computers", async () => {
    open("", (r) => { quiet(r); setDirect(r.direct, "mac", "failed", "no_traversal"); });
    await settle();
    expect(directRows()["mac"]).toBe("正在用中继 · 两边的网络隔开了，不能直接连通");
    expect(directRows()["mini"]).toBeUndefined();
  });

  it("shows an attempt in progress", async () => {
    open("", (r) => { quiet(r); setDirect(r.direct, "mac", "connecting"); });
    await settle();
    expect(directRows()["mac"]).toBe("正在建立直连…");
  });

  it("says which purposes go direct and never prints a speed or RTT on ConnCheck", async () => {
    open("", (r) => {
      quiet(r); setDirect(r.direct, "mac", "connected");
      const route = r.direct.routing("mac");
      route.fail({ kind: "read" });
      route.observe({ kind: "bulk", file: "f" }, "relay", 4 << 20, 1000);
      route.observe({ kind: "bulk", file: "f" }, "direct", 8 << 20, 500);
    });
    await settle();
    expect(directRows()["mac"]).toBe("正在用直连 · 用于文件");
    expect(host.textContent).not.toContain("MiB/s");
    expect(footer()).not.toMatch(/速度取自|实际传输/);
  });

  it("keeps the direct row plain without RTT suffix even when direct latency is measured", async () => {
    open("", (r) => {
      quiet(r); setDirect(r.direct, "mac", "connected");
      const route = r.direct.routing("mac");
      for (let i = 0; i < 3; i++) route.observe({ kind: "read" }, "direct", 0, 16);
    });
    await settle();
    expect(directRows()["mac"]).toBe("正在用直连");
  });

  const fallbackBoth = (r: DemoRemote) => {
    const route = r.direct.routing("mac");
    for (let i = 0; i < 8; i++) route.observe({ kind: "read" }, "direct", 0, 200);
    for (let i = 0; i < 4; i++) route.observe({ kind: "read" }, "relay", 0, 40);
    for (let i = 0; i < 3; i++) route.observe({ kind: "read" }, "relay", 0, 40);
    route.observe({ kind: "bulk", file: "f" }, "relay", 8 << 20, 1000);
    route.observe({ kind: "bulk", file: "f" }, "direct", 4 << 20, 1000);
  };

  it("says 正在用中继 · 直连较慢，暂时改用中继 when both purposes fell back", async () => {
    open("", (r) => { quiet(r); setDirect(r.direct, "mac", "connected"); fallbackBoth(r); });
    await settle();
    expect(directRows()["mac"]).toBe("正在用中继 · 直连较慢，暂时改用中继");
  });

  it("starts on direct right after connecting, with no measuring state", async () => {
    open("", (r) => { quiet(r); setDirect(r.direct, "mac", "connected"); });
    await settle();
    expect(directRows()["mac"]).toBe("正在用直连");
  });

  it("draws one mark on the machine row and no status mark on the plain direct summary row", async () => {
    open("", (r) => { quiet(r); setDirect(r.direct, "mac", "connected"); });
    await settle();
    const row = [...host.querySelectorAll(".conn-check .mi:not(.conn-direct)")].find((e) => e.textContent!.startsWith("MacBook"))!;
    expect(row.querySelectorAll(".mark").length).toBe(1);
    expect(row.querySelector(".mt .mark")).toBeNull();
    expect(row.querySelector(".mv .mark")).not.toBeNull();
    const directRow = host.querySelector('.conn-check .mi.conn-direct[data-machine-id="mac"]')!;
    expect(directRow.querySelectorAll(".mark").length).toBe(0);
  });

  it("says 直连已关闭 when this device turned it off", async () => {
    open("", (r) => { quiet(r); r.direct.setEnabled("mac", false); });
    await settle();
    expect(directRows()["mac"]).toBe("直连已关闭");
  });

  it("adds the unsupported browser sentence once in the footer, not on every computer", async () => {
    open("", quiet);
    await settle();
    expect(footer()).toBe(base() + "");
    act(() => root?.unmount()); host.remove();
    vi.stubGlobal("RTCPeerConnection", undefined);
    open("", quiet);
    await settle();
    expect(directRows()["mac"]).toBeUndefined();
    expect(directRows()["vps"]).toBeUndefined();
    expect(footer()).toBe(base() + "这个浏览器不支持直连，已全部经中继连接。");
  });

  it("separates the footer's sentences with spaces in English", async () => {
    localeStore.overrideForTesting("en");
    try {
      vi.stubGlobal("RTCPeerConnection", undefined);
      open("", quiet);
      await settle();
      expect(footer()).toBe(`${base()} This browser does not support direct connections; everything connects via relay.`);
    } finally { localeStore.overrideForTesting(undefined); }
  });

  it("updates when the engine reports a change", async () => {
    const r = open("", (x) => { quiet(x); setDirect(x.direct, "mac", "connecting"); });
    await settle();
    expect(directRows()["mac"]).toBe("正在建立直连…");
    act(() => { setDirect(r.direct, "mac", "connected"); fallbackBoth(r); (r.direct as unknown as { notify(): void }).notify(); });
    expect(directRows()["mac"]).toBe("正在用中继 · 直连较慢，暂时改用中继");
  });
});
