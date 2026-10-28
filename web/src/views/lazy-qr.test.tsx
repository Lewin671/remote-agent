// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { PairCodePage } from "./DeviceManagement";
const qr = vi.hoisted(() => ({ generate: vi.fn<(url: string) => Promise<string>>() }));
vi.mock("../qr-code", () => ({ pairingQRCode: qr.generate }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("loads QR generation on demand and reuses the result on unrelated updates", async () => {
  // Control the async resource itself. Waiting for a second Vite dynamic
  // import used to charge cold module loading to this test's 5-second budget.
  let finish!: (src: string) => void;
  qr.generate.mockReturnValue(new Promise<string>((resolve) => { finish = resolve; }));
  const remote = DemoRemote.create(new URLSearchParams("role=authority"), async () => {});
  remote.start();
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(<PairCodePage remote={remote} onBack={() => {}} />));
    expect(qr.generate).not.toHaveBeenCalled();
    await act(async () => [...host.querySelectorAll("button")].find(b => b.textContent === "生成配对码")!.click());
    expect(qr.generate).toHaveBeenCalledExactlyOnceWith("https://app.example.com/#/pair?code=12345678");
    expect(host.querySelector(".security-qr")).toBeNull();
    await act(async () => remote.touch());
    expect(qr.generate).toHaveBeenCalledTimes(1);
    await act(async () => finish("data:image/gif;base64,synthetic"));
    expect(host.querySelector(".security-qr")?.getAttribute("src")).toBe("data:image/gif;base64,synthetic");
    await act(async () => remote.touch());
    expect(qr.generate).toHaveBeenCalledTimes(1);
  } finally {
    await act(async () => root.unmount()); host.remove(); remote.stop();
    qr.generate.mockReset();
  }
});
