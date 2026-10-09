// @vitest-environment happy-dom
// 连接检测: each stretch's round trip, measured when the sheet opens.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
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
const rows = () => Object.fromEntries([...host.querySelectorAll(".conn-check .sr")].map((e) => [e.firstElementChild!.firstChild!.textContent, e.lastElementChild!.textContent!.trim()]));

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
    expect(rows()["服务器"]).toBe("检测中…");
    await vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>(".conn-again button")!.disabled).toBe(false));
    const got = rows();
    expect(got["本机网络"]).toBe("正常");
    // One slow sample of three does not make the line slow.
    expect(got["服务器"]).toBe("95 ms");
    expect(got["MacBook"]).toBe("180 ms");
    expect(got["VPS-dev"]).toBe("2140 ms · 较慢");
    expect(got["Mac mini"]).toBe("不在线");
  });

  it("says the server did not answer, and leaves the computers unmeasured", async () => {
    let asked = 0;
    open("", (r) => {
      r.pingServer = async () => Promise.reject(new Error("没有回应"));
      r.pingMachine = async () => ++asked;
    });
    await vi.waitFor(() => expect(rows()["服务器"]).toBe("没有回应"));
    expect(rows()["MacBook"]).toBe("未检测");
    expect(asked).toBe(0);
  });
});
