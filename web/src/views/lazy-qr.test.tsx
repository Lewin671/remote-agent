// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { PairCodePage } from "./DeviceManagement";
const qr = vi.hoisted(() => ({ loads: 0, make: vi.fn() }));
vi.mock("qrcode-generator", () => {
  qr.loads++;
  return { default: () => ({ addData() {}, make: qr.make, createDataURL: () => "data:image/gif;base64,synthetic" }) };
});
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
it("loads QR generation on demand and reuses the result on unrelated updates", async () => {
  const remote = DemoRemote.create(new URLSearchParams("role=authority"), async () => {});
  remote.start();
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(<PairCodePage remote={remote} onBack={() => {}} />));
    expect(qr.loads).toBe(0);
    await act(async () => [...host.querySelectorAll("button")].find(b => b.textContent === "生成配对码")!.click());
    // The actual async module boundary completes before checking the image.
    await act(async () => { await import("qrcode-generator"); });
    expect(qr.loads).toBe(1);
    expect(qr.make).toHaveBeenCalledTimes(1);
    expect(host.querySelector(".security-qr")?.getAttribute("src")).toContain("data:image/gif");
    await act(async () => remote.touch());
    expect(qr.make).toHaveBeenCalledTimes(1);
  } finally {
    await act(async () => root.unmount()); host.remove(); remote.stop();
  }
});
