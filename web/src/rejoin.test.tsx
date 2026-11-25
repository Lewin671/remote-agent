import { t as message } from "./i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";
import { errorText } from "./remote";
import { joinBanner } from "./views/Pairing";
import { RejoinButton } from "./Rejoin";
import { App } from "./App";
import { Remote } from "./remote";
import * as securityClient from "./core/security-client";
import { create } from "@bufbuild/protobuf";
import {
  SecurityBootstrapKind,
  SecurityExitAdviceSchema,
  SecurityWorkflowStatusSchema,
} from "./gen/remoteagent/core/v1/security_pb";
import { TextSchema } from "./gen/remoteagent/core/v1/text_pb";
import { RelayErrorDetailSchema, RelayErrorReason } from "./gen/remoteagent/v1/relay_pb";
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
afterEach(() => { if (root) act(() => root.unmount()); host?.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("keeps the login denial distinct from a bad pairing code", () => {
  const error = new ConnectError("login failed", Code.PermissionDenied, undefined, [
    { desc: RelayErrorDetailSchema, value: { reason: RelayErrorReason.LOGIN_FAILED } },
  ]);
  const text = "服务器不认识这台设备（可能已被移除，或服务已重建）";
  expect(errorText(error)).toBe(text);
  expect(joinBanner(error).text).toBe(text);
  expect(errorText(new ConnectError("operation denied", Code.PermissionDenied))).toBe("operation denied");
});

it.each([false, true])("rejoin clears data and goes directly to pairing; pending=%s", async pending => {
  const clearSecurity = vi.fn(async () => {});
  vi.spyOn(securityClient, "createStandaloneSecurityClient").mockReturnValue({
    client: {
      bootstrap: vi.fn(async () => create(SecurityWorkflowStatusSchema, {
        bootstrap: pending ? SecurityBootstrapKind.PENDING_REGISTRATION : SecurityBootstrapKind.READY,
      })),
      checkExitAdvice: vi.fn(async () => create(SecurityExitAdviceSchema, {
        authority: false,
        unresolved: pending,
        messageText: create(TextSchema, { literal: "原操作尚未完成。" }),
      })),
      clearSecurity,
    } as unknown as securityClient.SecurityClient,
    dispose: vi.fn(),
  });
  const done = vi.fn();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root.render(<RejoinButton server="https://relay.example.com" onDone={done}>用配对码重新加入</RejoinButton>));
  await act(async () => host.querySelector("button")!.click());
  if (pending) {
    expect(clearSecurity).not.toHaveBeenCalled();
    expect(host.textContent).toContain("电脑上的会话不受影响");
    const confirm = [...host.querySelectorAll("button")].find(b => b.textContent === message("security.rejoin"))!;
    await act(async () => confirm.click());
  }
  expect(clearSecurity).toHaveBeenCalledOnce();
  expect(done).toHaveBeenCalledOnce();
  expect(location.hash).toBe("#/pair?");
  expect(localStorage.getItem("ra-server")).toBe("https://relay.example.com");
});

it("rejoin clears relay push subscription via provided remote before clearing keys", async () => {
  const clearSecurity = vi.fn(async () => {});
  const setPushSubscription = vi.fn(async () => {});
  vi.spyOn(securityClient, "createStandaloneSecurityClient").mockReturnValue({
    client: {
      bootstrap: vi.fn(async () => create(SecurityWorkflowStatusSchema, {
        bootstrap: SecurityBootstrapKind.READY,
        server: "https://relay.example.com",
      })),
      checkExitAdvice: vi.fn(async () => create(SecurityExitAdviceSchema, {
        authority: false,
        unresolved: false,
      })),
      clearSecurity,
    } as unknown as securityClient.SecurityClient,
    dispose: vi.fn(),
  });
  const done = vi.fn();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root.render(
    <RejoinButton server="https://relay.example.com" remote={{ setPushSubscription }} onDone={done}>用配对码重新加入</RejoinButton>,
  ));
  await act(async () => host.querySelector("button")!.click());
  expect(setPushSubscription).toHaveBeenCalledWith("", []);
  expect(clearSecurity).toHaveBeenCalledOnce();
  expect(done).toHaveBeenCalledOnce();
});

it.each([false, true])("startup decides before code entry and discards retained state on rejoin; denied=%s", async denied => {
  location.hash = "";
  let retainedDeviceId = "synthetic-device";
  const checkRetainedDenied = vi.fn(async () => denied);
  const clearSecurity = vi.fn(async () => { retainedDeviceId = ""; });
  vi.spyOn(securityClient, "createStandaloneSecurityClient").mockImplementation(() => ({
    client: {
      bootstrap: vi.fn(async () => create(SecurityWorkflowStatusSchema, {
        bootstrap: SecurityBootstrapKind.EMPTY,
        server: retainedDeviceId ? "https://relay.example.com" : "",
        deviceId: retainedDeviceId,
      })),
      checkRetainedDenied,
      checkExitAdvice: vi.fn(async () => create(SecurityExitAdviceSchema, {
        authority: false,
        unresolved: false,
      })),
      clearSecurity,
    } as unknown as securityClient.SecurityClient,
    dispose: vi.fn(),
  }));
  vi.spyOn(Remote, "load").mockResolvedValue(undefined);
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root.render(<App />));
  await vi.waitFor(async () => { await act(async () => {}); expect(host.textContent).toContain(denied ? message("security.the_server_does_not_recognize_this_device") : message("security.one_time_security_setup")); });
  expect(checkRetainedDenied).toHaveBeenCalledOnce();
  if (!denied) { expect(host.textContent).not.toContain(message("security.rejoin_with_a_pairing_code")); return; }
  expect(host.querySelector('input[aria-label="配对码"]')).toBeNull();
  await act(async () => host.querySelector("button")!.click());
  await vi.waitFor(async () => { await act(async () => {}); expect(host.querySelector('input[aria-label="配对码"]')).not.toBeNull(); });
  expect(host.textContent).not.toContain(message("security.one_time_security_setup"));
  expect(host.querySelector<HTMLInputElement>('input[aria-label="服务器"]')?.value).toBe("https://relay.example.com");
  expect(retainedDeviceId).toBe("");
});
