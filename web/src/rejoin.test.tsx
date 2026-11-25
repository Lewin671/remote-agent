// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";
import { t as message } from "./i18n";
import { errorText } from "./remote";
import { RejoinButton, RetainedIdentityDeniedScreen } from "./Rejoin";
import {
  AppRootStage,
  ExitConfirmStep,
  ExitIntentKind,
  type CoreHost,
} from "./core/host";
import {
  AppRootViewModelSchema,
  ExitConfirmViewSchema,
  RetainedDeniedViewSchema,
} from "./gen/remoteagent/core/v1/security_view_pb";
import { TextSchema } from "./gen/remoteagent/core/v1/text_pb";
import { RelayErrorDetailSchema, RelayErrorReason } from "./gen/remoteagent/v1/relay_pb";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let container: HTMLDivElement;
let root: Root;

afterEach(() => {
  if (root) act(() => root.unmount());
  container?.remove();
  vi.restoreAllMocks();
});

function makeFakeHost() {
  let exitVm = create(ExitConfirmViewSchema, { step: ExitConfirmStep.CLOSED });
  const appRootVm = create(AppRootViewModelSchema, {
    stage: AppRootStage.RETAINED_DENIED,
    server: "https://relay.example.com",
    retainedDenied: create(RetainedDeniedViewSchema, {
      server: "https://relay.example.com",
    }),
  });
  let ver = 0;
  const listeners = new Set<() => void>();
  const setExitVm = (next: typeof exitVm) => {
    exitVm = next;
    ver++;
    for (const fn of listeners) fn();
  };
  const host = {
    subscribe: (_t: unknown, fn: () => void) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    version: () => ver,
    viewAppRoot: () => appRootVm,
    viewExitConfirm: () => exitVm,
    exitRequest: vi.fn(),
    exitConfirm: vi.fn(),
    appRootDismissUpgradeNotice: vi.fn(),
  } as unknown as CoreHost;
  return { host, setExitVm };
}

it("keeps the login denial distinct from a generic permission denied error", () => {
  const error = new ConnectError("login failed", Code.PermissionDenied, undefined, [
    { desc: RelayErrorDetailSchema, value: { reason: RelayErrorReason.LOGIN_FAILED } },
  ]);
  expect(errorText(error)).toBe("服务器不认识这台设备（可能已被移除，或服务已重建）");
  expect(errorText(new ConnectError("operation denied", Code.PermissionDenied))).toBe("operation denied");
});

it("renders RetainedIdentityDeniedScreen and wires REJOIN_PAIRING request + confirmation", async () => {
  const { host, setExitVm } = makeFakeHost();
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);

  await act(async () => root.render(<RetainedIdentityDeniedScreen host={host} />));
  expect(container.textContent).toContain(message("security.the_server_does_not_recognize_this_device"));

  await act(async () => container.querySelector("button")!.click());
  expect(host.exitRequest).toHaveBeenCalledWith(ExitIntentKind.REJOIN_PAIRING, "https://relay.example.com");

  await act(async () => {
    setExitVm(
      create(ExitConfirmViewSchema, {
        intent: ExitIntentKind.REJOIN_PAIRING,
        step: ExitConfirmStep.AWAITING_CONFIRM,
        warningBody: create(TextSchema, { key: "security.this_deletes_the_account_keys_and_cache_on_this" }),
        showRejoinDeletesCacheNote: true,
      }),
    );
  });

  const confirmBtn = [...container.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.rejoin"),
  );
  expect(confirmBtn).toBeTruthy();
  await act(async () => confirmBtn!.click());
  expect(host.exitConfirm).toHaveBeenCalledWith(true);
});

it("RejoinButton dispatches REJOIN_PAIRING with the target server", async () => {
  const { host } = makeFakeHost();
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root.render(<RejoinButton host={host} server="https://relay.example.com">用配对码重新加入</RejoinButton>),
  );
  await act(async () => container.querySelector("button")!.click());
  expect(host.exitRequest).toHaveBeenCalledWith(ExitIntentKind.REJOIN_PAIRING, "https://relay.example.com");
});
