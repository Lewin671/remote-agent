// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";
import { App } from "./App";
import * as remote from "./remote";
import * as storage from "./storage";
vi.mock("./authority-setup", () => ({ authorityStartup: vi.fn(async () => ({ kind: "empty" })) }));
vi.mock("./security-operation", async importOriginal => ({ ...await importOriginal<object>(), authorityExitAdvice: vi.fn(async () => ({ message: "安全操作结果尚未核实，请先继续原操作；退出会删除唯一完成材料。" })) }));
vi.mock("./views/AuthoritySetup", () => ({ AccountEntry: () => <p>新建账户 加入已有账户 用恢复码找回管理权</p> }));
vi.mock("./views/Pairing", () => ({ Setup: () => <p>重新配对</p> }));
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
afterEach(() => vi.restoreAllMocks());
it.each([Code.PermissionDenied, Code.FailedPrecondition, Code.Unavailable])(
  "keeps failed registration until an explicit confirmed discard: %s",
  async (code) => {
    const resume = vi
      .spyOn(remote, "resumeRegistration")
      .mockRejectedValue(new ConnectError("synthetic refusal", code));
    vi.spyOn(storage, "watchAccountLogout").mockReturnValue(() => {});
    const forget = vi
      .spyOn(storage, "forgetAccount")
      .mockResolvedValue(undefined);
    vi.spyOn(storage, "loadAccount").mockResolvedValue(undefined);
    vi.spyOn(remote.Remote, "load").mockResolvedValue(undefined);
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<App />));
      expect(forget).not.toHaveBeenCalled();
      const button = (text: string) =>
        [...host.querySelectorAll("button")].find(
          (x) => x.textContent === text,
        )!;
      await act(async () => button("重试").click());
      expect(resume).toHaveBeenCalledTimes(2);
      expect(button("退出并重新开始")).toBeTruthy();
      await act(async () => button("退出并重新开始").click());
      expect(forget).not.toHaveBeenCalled();
      resume.mockResolvedValue(undefined);
      await act(async () => button("确认退出").click());
      expect(forget).toHaveBeenCalledTimes(1);
      expect(host.textContent).toContain("加入已有账户");
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  },
);
