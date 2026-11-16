import { t as message } from "../i18n";
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
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
});

function open(query: string, patch?: (r: DemoRemote) => void) {
  const r = DemoRemote.create(new URLSearchParams(query));
  r.start();
  patch?.(r);
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(<ConnCheck remote={r} onClose={() => undefined} />));
  return r;
}
const rows = () => Object.fromEntries([...host.querySelectorAll(".conn-check .mi")].map((e) => [e.firstElementChild!.firstChild!.textContent, e.lastElementChild!.textContent!.trim()]));

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

describe("direct path lines", () => {
  const lines = () => Object.fromEntries([...host.querySelectorAll(".conn-check .mi")].map((e) => [e.firstElementChild!.firstChild!.textContent, [...e.querySelectorAll(".mt small")].map((s) => s.textContent)]));
  const settle = () => vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>(".conn-again button")!.disabled).toBe(false));
  const quiet = (r: DemoRemote) => { r.pingServer = async () => 40; r.pingMachine = async () => 180; };
  beforeEach(() => vi.stubGlobal("RTCPeerConnection", class {}));
  afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });

  it("says plainly that a computer has no direct connection yet, with no numbers", async () => {
    open("", quiet);
    await settle();
    expect(lines()["MacBook"]).toEqual([message("transport.status_none")]);
    expect(host.querySelector(".gf")!.textContent).not.toContain(message("transport.speed_footnote"));
  });

  it("names the cause when a round failed, and says nothing for an offline computer", async () => {
    open("", (r) => { quiet(r); setDirect(r.direct, "mac", "failed", "no_traversal"); });
    await settle();
    expect(lines()["MacBook"]).toEqual([`${message("transport.status_none")} · ${message("transport.reason_no_traversal")}`]);
    expect(lines()["Mac mini"]).toEqual([]);
  });

  it("shows an attempt in progress", async () => {
    open("", (r) => { quiet(r); setDirect(r.direct, "mac", "connecting"); });
    await settle();
    expect(lines()["MacBook"]).toEqual([message("transport.status_connecting")]);
  });

  it("shows which purposes use the path and only the speeds that were measured", async () => {
    open("", (r) => {
      quiet(r); setDirect(r.direct, "mac", "connected");
      const route = r.direct.routing("mac");
      route.filePath = "direct";
      route.observe({ kind: "bulk", file: "f" }, "relay", 4 << 20, 1000);
      route.observe({ kind: "bulk", file: "f" }, "direct", 8 << 20, 500);
    });
    await settle();
    const head = [message("transport.status_connected"), message("transport.file_direct"), message("transport.read_relay")].join(" · ");
    expect(lines()["MacBook"]).toEqual([head, "文件速度：直连 16.0 MiB/s，中继 4.0 MiB/s"]);
    expect(host.querySelector(".gf")!.textContent).toContain(message("transport.speed_footnote"));
  });

  it("shows a measured relay-only speed without inventing a direct one", async () => {
    open("", (r) => {
      quiet(r); setDirect(r.direct, "mac", "connected");
      r.direct.routing("mac").observe({ kind: "bulk", file: "f" }, "relay", 4 << 20, 2000);
    });
    await settle();
    expect(lines()["MacBook"]![1]).toBe("文件速度：中继 2.0 MiB/s");
  });

  it("says so when this device cannot do it, and when the user turned it off", async () => {
    open("", (r) => { quiet(r); r.direct.setEnabled("mac", false); });
    await settle();
    expect(lines()["MacBook"]).toEqual([`${message("transport.status_none")} · ${message("transport.reason_disabled")}`]);
    act(() => root?.unmount()); host.remove();
    vi.stubGlobal("RTCPeerConnection", undefined);
    open("", quiet);
    await settle();
    expect(lines()["MacBook"]).toEqual([message("transport.status_unsupported")]);
  });

  it("updates when the engine reports a change", async () => {
    const r = open("", (x) => { quiet(x); setDirect(x.direct, "mac", "connecting"); });
    await settle();
    expect(lines()["MacBook"]).toEqual([message("transport.status_connecting")]);
    act(() => { setDirect(r.direct, "mac", "connected"); (r.direct as unknown as { notify(): void }).notify(); });
    expect(lines()["MacBook"]![0]).toContain(message("transport.status_connected"));
  });
});
