// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { t as message } from "../i18n";
import { AccountEntry } from "./AuthoritySetup";
import {
  CreateAccountStep,
  OnboardingMode,
  OnboardingRecoveryActionKind,
  SecurityRecoveryPhase,
  type CoreHost,
} from "../core/host";
import {
  CreateAccountViewSchema,
  EmergencyRecoveryViewSchema,
  ExitConfirmViewSchema,
  JoinPairingViewSchema,
  OnboardingViewModelSchema,
} from "../gen/remoteagent/core/v1/security_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let container: HTMLDivElement;
let root: Root;

afterEach(() => {
  if (root) act(() => root.unmount());
  container?.remove();
  vi.clearAllMocks();
});

function makeFakeHost(initial = create(OnboardingViewModelSchema, { mode: OnboardingMode.ENTRY })) {
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
    viewOnboarding: () => vm,
    viewExitConfirm: () => create(ExitConfirmViewSchema, {}),
    onboardingSetMode: vi.fn(),
    onboardingCreateEdit: vi.fn(),
    onboardingCreateSubmit: vi.fn(),
    onboardingRecoveryEdit: vi.fn(),
    onboardingRecoveryAction: vi.fn(),
    appRootDismissUpgradeNotice: vi.fn(),
    exitRequest: vi.fn(),
    exitConfirm: vi.fn(),
  } as unknown as CoreHost;
  return { host, setVm };
}

const button = (label: string) =>
  [...container.querySelectorAll("button")].find(
    (b) => (b.getAttribute("aria-label") ?? b.textContent) === label,
  )!;

it("renders three unauthenticated entries and dispatches mode transitions + upgrade notice dismissal", async () => {
  const { host } = makeFakeHost(
    create(OnboardingViewModelSchema, {
      mode: OnboardingMode.ENTRY,
      showLegacyRepairBanner: true,
    }),
  );
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root.render(<AccountEntry host={host} />));

  for (const label of ["创建账户", "加入已有账户", "用恢复码找回管理权"]) {
    expect(button(label)).toBeTruthy();
  }
  await act(async () => button(message("common.got_it")).click());
  expect(host.appRootDismissUpgradeNotice).toHaveBeenCalledOnce();

  await act(async () => button(message("security.create_account")).click());
  expect(host.onboardingSetMode).toHaveBeenCalledWith(OnboardingMode.CREATE);
});

it("renders create-account backup step and passes setup token on submit without storing it in Core", async () => {
  const { host } = makeFakeHost(
    create(OnboardingViewModelSchema, {
      mode: OnboardingMode.CREATE,
      canBack: false,
      create: create(CreateAccountViewSchema, {
        step: CreateAccountStep.BACKUP_CODE,
        serverInput: "https://ra.example.com",
        recoveryCode: "ABCD-EFGH-IJKL",
        codeSavedChecked: true,
        canSubmit: true,
        showSetupReset: true,
        hasPendingCreate: true,
      }),
    }),
  );
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root.render(<AccountEntry host={host} />));

  expect(container.textContent).toContain("ABCD-EFGH-IJKL");
  const tokInput = container.querySelector<HTMLInputElement>('input[aria-label="Setup token"]')!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(tokInput, "secret-tok");
    tokInput.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => button(message("security.continue_creation")).click());
  expect(host.onboardingCreateSubmit).toHaveBeenCalledWith({
    serverInput: "https://ra.example.com",
    setupToken: "secret-tok",
    confirmCodeSaved: true,
  });
});

it("renders recovery waiting/ready phases from Core SecurityRecoveryPhase enum", async () => {
  const { host } = makeFakeHost(
    create(OnboardingViewModelSchema, {
      mode: OnboardingMode.RECOVERY,
      recovery: create(EmergencyRecoveryViewSchema, {
        phase: SecurityRecoveryPhase.READY_TO_COMPLETE,
        canComplete: true,
        statusHintText: create(TextSchema, { key: "security.recovery_request_pending" }),
      }),
    }),
  );
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root.render(<AccountEntry host={host} />));

  await act(async () => button(message("security.complete_recovery")).click());
  expect(host.onboardingRecoveryAction).toHaveBeenCalledWith(OnboardingRecoveryActionKind.COMPLETE);
});

it("renders join screen when mode is JOIN", async () => {
  const { host } = makeFakeHost(
    create(OnboardingViewModelSchema, {
      mode: OnboardingMode.JOIN,
      canBack: true,
      join: create(JoinPairingViewSchema, {
        serverInput: "https://ra.example.com",
        serverEditable: true,
      }),
    }),
  );
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root.render(<AccountEntry host={host} />);
    await import("./Pairing");
  });
  expect(container.textContent).toContain(message("security.join_an_existing_account"));
});
