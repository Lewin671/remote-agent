// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { t as message } from "./i18n";

const mockPush = vi.hoisted(() => ({
  unregisterPushOnExit: vi.fn(async () => {}),
  createStandalonePushUnsubscriber: vi.fn(() => ({ setPushSubscription: vi.fn(async () => {}) })),
}));

vi.mock("./push", () => ({
  unregisterPushOnExit: mockPush.unregisterPushOnExit,
  createStandalonePushUnsubscriber: mockPush.createStandalonePushUnsubscriber,
}));

const mockClient = vi.hoisted(() => ({
  bootstrap: vi.fn(),
  checkExitAdvice: vi.fn(),
  clearSecurity: vi.fn(),
  syncNow: vi.fn(),
  dispose: vi.fn(),
}));

vi.mock("./core/security-client", () => ({
  createStandaloneSecurityClient: () => ({
    client: {
      bootstrap: mockClient.bootstrap,
      checkExitAdvice: mockClient.checkExitAdvice,
      clearSecurity: mockClient.clearSecurity,
      syncNow: mockClient.syncNow,
    },
    dispose: mockClient.dispose,
  }),
  translateSecurityKey: (k: string) => k,
}));

import { SetupReset } from "./SetupReset";

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

afterEach(() => {
  if (root) act(() => root.unmount());
  host?.remove();
  vi.clearAllMocks();
});

it("opens confirmation and clears corrupt local security storage even when bootstrap fails", async () => {
  mockClient.bootstrap.mockRejectedValue(new Error("corrupt record"));
  mockClient.checkExitAdvice.mockResolvedValue({ authority: false, unresolved: false, message: "generic" });
  mockClient.clearSecurity.mockResolvedValue(undefined);
  const onDone = vi.fn();

  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<SetupReset onDone={onDone} />));

  const startBtn = [...host.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.exit_and_start_over"),
  );
  expect(startBtn).toBeTruthy();

  await act(async () => {
    startBtn!.click();
  });

  // Confirmation modal must open despite bootstrap() throwing.
  const confirmBtn = [...host.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.confirm_exit"),
  );
  expect(confirmBtn).toBeTruthy();

  await act(async () => {
    confirmBtn!.click();
  });

  expect(mockClient.clearSecurity).toHaveBeenCalledTimes(1);
  expect(onDone).toHaveBeenCalledTimes(1);
});

it("preserves sole-authority exit warning when bootstrap fails but record still identifies authority", async () => {
  const authMsg = "security.this_device_is_the_only_managing_client_transfer_management";
  mockClient.bootstrap.mockRejectedValue(new Error("missing key"));
  mockClient.checkExitAdvice.mockResolvedValue({
    authority: true,
    unresolved: false,
    message: authMsg,
  });

  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<SetupReset onDone={vi.fn()} />));

  const startBtn = [...host.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.exit_and_start_over"),
  );
  await act(async () => {
    startBtn!.click();
  });

  expect(mockClient.checkExitAdvice).toHaveBeenCalledTimes(1);
  expect(host.textContent).toContain(authMsg);
});

it("passes standalone relay push unsubscriber when SetupReset knows the server", async () => {
  mockClient.bootstrap.mockResolvedValue({ server: "https://relay.example.com" });
  mockClient.checkExitAdvice.mockResolvedValue({ authority: false, unresolved: false, message: "ok" });
  mockClient.clearSecurity.mockResolvedValue(undefined);

  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<SetupReset onDone={vi.fn()} />));

  const startBtn = [...host.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.exit_and_start_over"),
  );
  await act(async () => {
    startBtn!.click();
  });
  const confirmBtn = [...host.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.confirm_exit"),
  );
  await act(async () => {
    confirmBtn!.click();
  });

  expect(mockPush.createStandalonePushUnsubscriber).toHaveBeenCalledWith(
    expect.objectContaining({ server: "https://relay.example.com" }),
  );
  expect(mockPush.unregisterPushOnExit).toHaveBeenCalledWith(
    expect.objectContaining({ setPushSubscription: expect.any(Function) }),
  );
  expect(mockClient.clearSecurity).toHaveBeenCalledTimes(1);
});
