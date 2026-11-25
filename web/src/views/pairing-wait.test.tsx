// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { t as message } from "../i18n";
import {
  JoinPairingStep,
  OnboardingMode,
  SecurityBannerAction,
  SecurityBannerTone,
  type CoreHost,
} from "../core/host";
import {
  ExitConfirmViewSchema,
  JoinPairingViewSchema,
  OnboardingViewModelSchema,
  SecurityBannerViewSchema,
} from "../gen/remoteagent/core/v1/security_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import { Setup } from "./Pairing";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let container: HTMLElement;

afterEach(() => {
  act(() => root?.unmount());
  container?.remove();
  vi.clearAllMocks();
});

function makeFakeHost(joinPatch: Parameters<typeof create<typeof JoinPairingViewSchema>>[1]) {
  let vm = create(OnboardingViewModelSchema, {
    mode: OnboardingMode.JOIN,
    join: create(JoinPairingViewSchema, joinPatch),
  });
  let ver = 0;
  const listeners = new Set<() => void>();
  const setJoin = (next: Parameters<typeof create<typeof JoinPairingViewSchema>>[1]) => {
    vm = create(OnboardingViewModelSchema, {
      mode: OnboardingMode.JOIN,
      join: create(JoinPairingViewSchema, next),
    });
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
    onboardingJoinEdit: vi.fn(),
    onboardingJoinSubmit: vi.fn(),
    onboardingJoinConfirmSas: vi.fn(),
    onboardingJoinCancel: vi.fn(),
    onboardingJoinBannerAction: vi.fn(),
  } as unknown as CoreHost;
  return { host, setJoin };
}

it("renders join form, forwards edits/submit, and renders SAS compare + cancel", async () => {
  const { host, setJoin } = makeFakeHost({
    step: JoinPairingStep.FORM,
    formattedCode: "4829 1756",
    serverInput: "https://ra.example.com",
    serverEditable: true,
    canSubmit: true,
  });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root!.render(<Setup host={host} />));

  const submitBtn = [...container.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.join"),
  )!;
  await act(async () => submitBtn.click());
  expect(host.onboardingJoinSubmit).toHaveBeenCalledOnce();

  await act(async () => {
    setJoin({
      step: JoinPairingStep.COMPARE_SAS,
      sasEmoji: ["🦊", "🌵", "🚲", "🎈"],
      canConfirmSas: true,
      canCancel: true,
    });
  });

  expect(container.textContent).toContain("🦊 🌵 🚲 🎈");
  const confirmBtn = [...container.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.patterns_match_trust_and_continue"),
  )!;
  await act(async () => confirmBtn.click());
  expect(host.onboardingJoinConfirmSas).toHaveBeenCalledOnce();

  const cancelBtn = [...container.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.patterns_differ_return_to_code_entry"),
  )!;
  await act(async () => cancelBtn.click());
  expect(host.onboardingJoinCancel).toHaveBeenCalledOnce();
});

it("renders Core structured join banner and dispatches banner action", async () => {
  const { host } = makeFakeHost({
    step: JoinPairingStep.FORM,
    serverEditable: true,
    banner: create(SecurityBannerViewSchema, {
      visible: true,
      tone: SecurityBannerTone.ERROR,
      text: create(TextSchema, { key: "security.the_pairing_code_is_wrong_or_has_already_been" }),
      action: SecurityBannerAction.REFILL,
    }),
  });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root!.render(<Setup host={host} />));

  expect(container.textContent).toContain(message("security.the_pairing_code_is_wrong_or_has_already_been"));
  const actBtn = [...container.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.re_enter"),
  )!;
  await act(async () => actBtn.click());
  expect(host.onboardingJoinBannerAction).toHaveBeenCalledOnce();
});
