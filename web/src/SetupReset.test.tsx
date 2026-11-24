// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { t as message } from "./i18n";

const mockClient = vi.hoisted(() => ({
  bootstrap: vi.fn(),
  checkExitAdvice: vi.fn(),
  clearSecurity: vi.fn(),
  dispose: vi.fn(),
}));

vi.mock("./core/security-client", () => ({
  createStandaloneSecurityClient: () => ({
    client: {
      bootstrap: mockClient.bootstrap,
      checkExitAdvice: mockClient.checkExitAdvice,
      clearSecurity: mockClient.clearSecurity,
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
