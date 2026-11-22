import { t as message } from "./i18n";
// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";
import { App } from "./App";
import * as remote from "./remote";
import * as securityClient from "./core/security-client";
import { SecurityBootstrapKind, SecurityWorkflowStatusSchema } from "./gen/remoteagent/core/v1/security_pb";
vi.mock("./views/AuthoritySetup", () => ({ AccountEntry: () => <p>创建账户 加入已有账户 用恢复码找回管理权</p> }));
vi.mock("./views/Pairing", () => ({ Setup: () => <p>重新配对</p> }));
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
afterEach(() => vi.restoreAllMocks());
it.each([Code.PermissionDenied, Code.FailedPrecondition, Code.Unavailable])(
  "keeps failed registration until an explicit confirmed discard: %s",
  async (code) => {
    const resumePendingOp = vi
      .fn()
      .mockRejectedValueOnce(new ConnectError("synthetic refusal", code))
      .mockRejectedValueOnce(new ConnectError("synthetic refusal", code));
    const clearSecurity = vi.fn().mockResolvedValue(undefined);
    let cleared = false;
    vi.spyOn(securityClient, "createStandaloneSecurityClient").mockImplementation(() => ({
      client: {
        bootstrap: vi.fn(async () => create(SecurityWorkflowStatusSchema, {
          bootstrap: cleared ? SecurityBootstrapKind.EMPTY : SecurityBootstrapKind.PENDING_REGISTRATION,
        })),
        resumePendingOp,
        checkExitAdvice: vi.fn(async () => ({
          message: "安全操作结果尚未核实，请先继续原操作；退出会删除唯一完成材料。",
        })),
        clearSecurity: async () => {
          cleared = true;
          await clearSecurity();
        },
      } as unknown as securityClient.SecurityClient,
      dispose: vi.fn(),
    }));
    vi.spyOn(remote.Remote, "load").mockResolvedValue(undefined);
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<App />));
      expect(clearSecurity).not.toHaveBeenCalled();
      const button = (text: string) =>
        [...host.querySelectorAll("button")].find(
          (x) => x.textContent === text,
        )!;
      await act(async () => button(message("common.retry")).click());
      expect(resumePendingOp).toHaveBeenCalledTimes(2);
      expect(button(message("security.exit_and_start_over"))).toBeTruthy();
      await act(async () => button(message("security.exit_and_start_over")).click());
      expect(clearSecurity).not.toHaveBeenCalled();
      await act(async () => button(message("security.confirm_exit")).click());
      expect(clearSecurity).toHaveBeenCalledTimes(1);
      expect(host.textContent).toContain(message("security.join_an_existing_account"));
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  },
);
