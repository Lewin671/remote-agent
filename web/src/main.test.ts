// @vitest-environment happy-dom
import { expect, it, vi } from "vitest";

vi.mock("./app-entry", () => { throw new TypeError("network"); });
vi.mock("./theme", () => ({ applyTheme: vi.fn(), applyFonts: vi.fn() }));

it("keeps the shell and offers a retry when the app cannot download", async () => {
  document.body.innerHTML = '<div id="root"><h1>remote-agent</h1><p id="startup-status">正在打开…</p><a id="startup-retry" href="" hidden>重新加载</a></div>';
  try {
    await import("./main");
    await vi.waitFor(() => expect(document.getElementById("startup-retry")!.hidden).toBe(false));
    expect(document.querySelector("h1")!.textContent).toBe("remote-agent");
    expect(document.getElementById("startup-status")!.textContent).toContain("检查网络");
  } finally {
    document.body.innerHTML = "";
  }
});
