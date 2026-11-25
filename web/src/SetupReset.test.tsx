// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { t as message } from "./i18n";
import {
  ExitConfirmStep,
  ExitIntentKind,
  type CoreHost,
} from "./core/host";
import { ExitConfirmViewSchema } from "./gen/remoteagent/core/v1/security_view_pb";
import { TextSchema } from "./gen/remoteagent/core/v1/text_pb";
import { SetupReset } from "./SetupReset";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

afterEach(() => {
  if (root) act(() => root.unmount());
  container?.remove();
  vi.clearAllMocks();
});

function makeFakeHost(initial = create(ExitConfirmViewSchema, { step: ExitConfirmStep.CLOSED })) {
  let vm = initial;
  let ver = 0;
  const listeners = new Set<() => void>();
  const setVm = (next: typeof vm) => {
    vm = next;
    ver++;
    for (const fn of listeners) fn();
  };
  const host = {
    subscribe: (_t: unknown, fn: () => void) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    version: () => ver,
    viewExitConfirm: () => vm,
    exitRequest: vi.fn(),
    exitConfirm: vi.fn(),
  } as unknown as CoreHost;
  return { host, setVm };
}

it("dispatches SETUP_RESET request and renders core-provided exit warning and confirmation", async () => {
  const { host, setVm } = makeFakeHost();
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root.render(<SetupReset host={host} />));

  const startBtn = [...container.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.exit_and_start_over"),
  );
  expect(startBtn).toBeTruthy();

  await act(async () => startBtn!.click());
  expect(host.exitRequest).toHaveBeenCalledWith(ExitIntentKind.SETUP_RESET);

  await act(async () => {
    setVm(
      create(ExitConfirmViewSchema, {
        intent: ExitIntentKind.SETUP_RESET,
        step: ExitConfirmStep.AWAITING_CONFIRM,
        warningBody: create(TextSchema, { key: "security.this_device_is_the_only_managing_client_transfer_management" }),
      }),
    );
  });

  expect(container.textContent).toContain(
    message("security.this_device_is_the_only_managing_client_transfer_management"),
  );
  const confirmBtn = [...container.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.confirm_exit"),
  );
  expect(confirmBtn).toBeTruthy();
  await act(async () => confirmBtn!.click());
  expect(host.exitConfirm).toHaveBeenCalledWith(true);
});
